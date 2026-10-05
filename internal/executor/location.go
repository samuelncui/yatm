package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/ignore"
	"github.com/samuelncui/yatm/internal/library"
)

// ErrAccessExcluded identifies intentional boundary and resource exclusions, not inspection failures.
var ErrAccessExcluded = errors.New("path is excluded from permitted access")

// AccessRanges returns configured administrator boundaries.
func (e *Executor) AccessRanges() []AccessRange {
	return append([]AccessRange{}, e.paths.Access...)
}

// LocationRoot validates a directory against administrator boundaries without following user symlinks.
func (e *Executor) LocationRoot(value string) (string, error) {
	// A relative input is only unambiguous when a single administrator boundary exists.
	ranges := e.access
	if !filepath.IsAbs(value) {
		if len(ranges) != 1 {
			return "", fmt.Errorf("an absolute directory path is required")
		}
		value = filepath.Join(ranges[0].root, value)
	}
	value, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	var failures, exclusions []error
	for _, access := range ranges {
		root, err := authorizedDirectory(access.root, access.matcher, value)
		if err != nil {
			if errors.Is(err, ErrAccessExcluded) {
				exclusions = append(exclusions, err)
			} else {
				failures = append(failures, err)
			}
			continue
		}
		return root, nil
	}
	if len(failures) > 0 {
		return "", fmt.Errorf("inspect permitted directory %q failed: %w", value, errors.Join(failures...))
	}
	if len(exclusions) > 0 {
		return "", fmt.Errorf("directory %q is not permitted: %w", value, errors.Join(exclusions...))
	}
	return "", fmt.Errorf("directory %q is outside permitted access: %w", value, ErrAccessExcluded)
}

func authorizedDirectory(root string, matcher *ignore.Matcher, value string) (string, error) {
	// Canonicalize only the administrator-owned root; descendants may not traverse symlinks.
	base, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", fmt.Errorf("resolve source access boundary failed, %w", err)
	}
	relative, err := filepath.Rel(base, filepath.Clean(value))
	if err != nil {
		return "", err
	}
	if !withinLocationPath(relative) {
		relative, err = filepath.Rel(canonical, filepath.Clean(value))
		if err != nil || !withinLocationPath(relative) {
			return "", fmt.Errorf("online root escapes source boundary %q: %w", value, ErrAccessExcluded)
		}
	}

	// Each discovered descendant must be a real directory; an inaccessible root is not empty.
	resolved := canonical
	if relative != "." {
		if matcher.Match(filepath.ToSlash(relative), true) {
			return "", fmt.Errorf("directory is excluded by administrator rules: %w", ErrAccessExcluded)
		}
		parts := strings.Split(relative, string(filepath.Separator))
		for _, part := range parts {
			resolved = filepath.Join(resolved, part)
			info, err := os.Lstat(resolved)
			if err != nil {
				return "", fmt.Errorf("inspect online root failed, %w", err)
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("online root is not a real directory %q: %w", resolved, ErrAccessExcluded)
			}
		}
	}
	directory, err := os.Open(resolved)
	if err != nil {
		return "", fmt.Errorf("open online root failed, %w", err)
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("online root is not a directory %q: %w", resolved, ErrAccessExcluded)
	}
	return resolved, nil
}

func withinLocationPath(relative string) bool {
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

// CanonicalConfiguredPath freezes a trusted configuration path, including nonexistent descendants.
func CanonicalConfiguredPath(value string) (string, error) {
	// Resolve existing administrator-owned ancestors even before a Job/SQLite sidecar is created.
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	ancestor, suffix := absolute, ""
	for {
		resolved, err := filepath.EvalSymlinks(ancestor)
		if err == nil {
			return filepath.Join(resolved, suffix), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", err
		}
		suffix = filepath.Join(filepath.Base(ancestor), suffix)
		ancestor = parent
	}
}

// CheckLocation applies the current installation access boundary at use time.
func (e *Executor) CheckLocation(source *library.Location) (string, error) {
	// A Location registered for another Executor cannot be read through this installation.
	if source.ExecutorID != localExecutorID {
		return "", library.ErrLocationUnverified
	}
	root, err := e.LocationRoot(source.RootPath)
	if err != nil {
		return "", err
	}
	if root != source.RootPath {
		return "", fmt.Errorf("online root is not the canonical path; update the Location")
	}

	source.AccessAllowed = e.originalAccess(root)
	return root, nil
}

type locationAccessRange struct {
	prefix  string
	matcher *ignore.Matcher
}

func (e *Executor) locationAccessRanges(root string) []locationAccessRange {
	// Bind only ranges containing the registered root, preserving explicit-path authorization.
	ranges := make([]locationAccessRange, 0, len(e.access))
	for _, access := range e.access {
		base, err := CanonicalConfiguredPath(access.root)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(base, root)
		if err != nil || !withinLocationPath(relative) {
			continue
		}
		if relative == "." {
			relative = ""
		}
		ranges = append(ranges, locationAccessRange{filepath.ToSlash(relative), access.matcher})
	}
	return ranges
}

func (e *Executor) originalAccess(root string) func(string, bool) bool {
	// Explicit reads and directory scopes share the registered-root range binding.
	ranges := e.locationAccessRanges(root)

	// Excluded ancestors cannot be implicitly reopened by an exception for a child.
	return func(relative string, directory bool) bool {
		for _, access := range ranges {
			joined := filepath.ToSlash(filepath.Join(access.prefix, relative))
			if !access.matcher.Match(joined, directory) {
				return true
			}
		}
		return false
	}
}

// ResolveOriginal returns the current validated path for one selected File's original.
func (e *Executor) ResolveOriginal(ctx context.Context, expected *entity.ExpectedFile) (string, int64, error) {
	if expected == nil || expected.FileId <= 0 {
		return "", 0, fmt.Errorf("selected File identity is missing")
	}

	// The one-to-one recorded original is authoritative; another File's equal content is not a fallback.
	original, err := e.lib.GetFileLocation(ctx, expected.FileId)
	if err != nil {
		return "", 0, err
	}
	if original == nil {
		return "", 0, fmt.Errorf("File %d has no original; automatic Restore is not supported", expected.FileId)
	}
	location, err := e.lib.GetLocation(ctx, original.LocationID)
	if err != nil {
		return "", 0, err
	}
	filename, info, err := e.CheckLocationPath(location, original.Path)
	if err != nil {
		return "", 0, fmt.Errorf("File %d original is unavailable: %w", expected.FileId, err)
	}
	if !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("File %d original is not an ordinary file", expected.FileId)
	}
	return filename, original.LocationID, nil
}
