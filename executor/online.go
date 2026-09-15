package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/ignore"
	"github.com/samuelncui/yatm/library"
	"github.com/samuelncui/yatm/media"
	"gorm.io/gorm"
)

// ErrAccessExcluded identifies intentional boundary and resource exclusions, not inspection failures.
var ErrAccessExcluded = errors.New("path is excluded from permitted access")

// SetOnlineRuntimePaths supplies process-owned resources outside the standard Job bundle tree.
// Configure this once before accepting requests.
func (e *Executor) SetOnlineRuntimePaths(paths ...string) {
	// Snapshot startup resources; changing log filenames use the provider below.
	e.onlineRuntimePaths = append([]string{}, paths...)
}

// SetOnlineRuntimePathProvider supplies changing process resources, such as rotated logs.
// Configure it once before accepting requests; the provider must be safe for concurrent reads.
func (e *Executor) SetOnlineRuntimePathProvider(provider func() []string) {
	// Leave resource discovery with the owner of the process configuration.
	e.onlineRuntimePathProvider = provider
}

// AccessRanges returns administrator boundaries, with legacy paths used only as migration defaults.
func (e *Executor) AccessRanges() []AccessRange {
	if e.paths.Access != nil {
		return append([]AccessRange{}, e.paths.Access...)
	}
	var ranges []AccessRange
	for _, root := range []string{e.paths.Source, e.paths.Target} {
		if root != "" {
			ranges = append(ranges, AccessRange{Root: root})
		}
	}
	return ranges
}

// OnlineRoot validates a directory against administrator boundaries without following user symlinks.
func (e *Executor) OnlineRoot(value string) (string, error) {
	// A relative input is only unambiguous when a single administrator boundary exists.
	ranges := e.AccessRanges()
	if !filepath.IsAbs(value) {
		if len(ranges) != 1 {
			return "", fmt.Errorf("an absolute directory path is required")
		}
		value = filepath.Join(ranges[0].Root, value)
	}
	value, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	if e.isTemporaryResource(value) {
		return "", fmt.Errorf("directory is an active YATM temporary resource: %w", ErrAccessExcluded)
	}
	var failures, exclusions []error
	for _, access := range ranges {
		root, err := authorizedDirectory(access, value)
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

func authorizedDirectory(access AccessRange, value string) (string, error) {
	// Canonicalize only the administrator-owned root; descendants may not traverse symlinks.
	base, err := filepath.Abs(access.Root)
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
	if !withinOnlinePath(relative) {
		relative, err = filepath.Rel(canonical, filepath.Clean(value))
		if err != nil || !withinOnlinePath(relative) {
			return "", fmt.Errorf("online root escapes source boundary %q: %w", value, ErrAccessExcluded)
		}
	}

	// Each discovered descendant must be a real directory; an inaccessible root is not empty.
	root := canonical
	matcher := ignore.Compile(access.Ignore)
	if relative != "." {
		if matcher.Match(filepath.ToSlash(relative), true) {
			return "", fmt.Errorf("directory is excluded by administrator rules: %w", ErrAccessExcluded)
		}
		parts := strings.Split(relative, string(filepath.Separator))
		for _, part := range parts {
			root = filepath.Join(root, part)
			info, err := os.Lstat(root)
			if err != nil {
				return "", fmt.Errorf("inspect online root failed, %w", err)
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("online root is not a real directory %q: %w", root, ErrAccessExcluded)
			}
		}
	}
	directory, err := os.Open(root)
	if err != nil {
		return "", fmt.Errorf("open online root failed, %w", err)
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("online root is not a directory %q: %w", root, ErrAccessExcluded)
	}
	return root, nil
}

func withinOnlinePath(relative string) bool {
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

// RequiredOnlineExclusions separates working originals from known YATM storage resources.
func (e *Executor) RequiredOnlineExclusions(root string) ([]string, error) {
	// Resolve actual Job/Preview roots and explicit process resources, not the whole work directory.
	resources := append([]string{filepath.Join(e.paths.Work, "jobs")}, e.onlineRuntimePaths...)
	if e.onlineRuntimePathProvider != nil {
		resources = append(resources, e.onlineRuntimePathProvider()...)
	}
	if provider, ok := e.previews.(interface{ StorageRoot() string }); ok {
		resources = append(resources, provider.StorageRoot())
	}
	volumes, err := media.ListVolumes(e.paths.Volumes)
	if err != nil {
		return nil, fmt.Errorf("check archive/source separation failed, %w", err)
	}
	for _, volume := range volumes {
		resources = append(resources, volume.Root)
	}

	// Descendant resources become explicit exclusions; a root inside a resource cannot be registered.
	var required []string
	for _, value := range resources {
		if value == "" {
			continue
		}
		absolute, err := CanonicalConfiguredPath(value)
		if err != nil {
			return nil, err
		}
		inside, err := filepath.Rel(absolute, root)
		if err != nil {
			return nil, err
		}
		if withinOnlinePath(inside) {
			return nil, fmt.Errorf("online root overlaps YATM storage resource %q: %w", absolute, ErrAccessExcluded)
		}
		relative, err := filepath.Rel(root, absolute)
		if err != nil {
			return nil, err
		}
		if withinOnlinePath(relative) {
			required = append(required, filepath.ToSlash(relative))
		}
	}

	// These protected resources are exact subtrees, independent of user gitignore syntax.
	sort.Strings(required)
	result := make([]string, 0, len(required))
	for _, relative := range required {
		covered := false
		for _, parent := range result {
			if relative == parent || strings.HasPrefix(relative, parent+"/") {
				covered = true
				break
			}
		}
		if !covered {
			result = append(result, relative)
		}
	}
	return result, nil
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

// CheckOnlineSource applies the current installation boundary and required exclusions at use time.
func (e *Executor) CheckOnlineSource(source *library.Location) (string, error) {
	// Imported foreign bindings need explicit reassignment to this installation first.
	if source.ExecutorID != localExecutorID {
		return "", library.ErrOnlineUnverified
	}
	root, err := e.OnlineRoot(source.RootPath)
	if err != nil {
		return "", err
	}
	if root != source.RootPath {
		return "", fmt.Errorf("online root binding changed; confirm the canonical path")
	}

	// Newly configured runtime/archive resources must not enter an existing source silently.
	required, err := e.RequiredOnlineExclusions(root)
	if err != nil {
		return "", err
	}
	source.RequiredExclusions = required
	source.AccessAllowed = e.originalAccess(root)
	return root, nil
}

func (e *Executor) originalAccess(root string) func(string, bool) bool {
	// Compile administrator patterns once per operation, outside the per-file traversal.
	type allowedRange struct {
		prefix  string
		matcher *ignore.Matcher
	}
	ranges := make([]allowedRange, 0, len(e.AccessRanges()))
	for _, access := range e.AccessRanges() {
		base, err := CanonicalConfiguredPath(access.Root)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(base, root)
		if err != nil || !withinOnlinePath(relative) {
			continue
		}
		if relative == "." {
			relative = ""
		}
		ranges = append(ranges, allowedRange{filepath.ToSlash(relative), ignore.Compile(access.Ignore)})
	}

	// Excluded ancestors cannot be implicitly reopened by an exception for a child.
	return func(relative string, directory bool) bool {
		if e.isTemporaryResource(filepath.Join(root, filepath.FromSlash(relative))) {
			return false
		}
		for _, access := range ranges {
			joined := filepath.ToSlash(filepath.Join(access.prefix, relative))
			if !access.matcher.Match(joined, directory) {
				return true
			}
		}
		return false
	}
}

// OpenOnlinePosition opens one observed identity using the same descriptor later served to the caller.
func (e *Executor) OpenOnlinePosition(ctx context.Context, position *library.OnlinePosition, expected *entity.ExpectedFile) (*os.File, error) {
	// Refuse mutable-position identity substitution before any filesystem access.
	if position.IsDir || expected == nil || position.FileID != expected.FileId {
		return nil, library.ErrOnlineConflict
	}
	if position.Size != expected.Size {
		return nil, library.ErrOnlineConflict
	}
	if len(position.Hash) > 0 && !bytes.Equal(position.Hash, expected.Sha256) {
		return nil, library.ErrOnlineConflict
	}
	if len(position.Signature) > 0 && !bytes.Equal(position.Signature, expected.Signature) {
		return nil, library.ErrOnlineConflict
	}
	source, err := e.lib.GetOnlineSource(ctx, position.SourceID)
	if err != nil {
		return nil, err
	}
	if source.Binding != entity.OnlineBinding_CONFIRMED || source.BindingToken == "" || position.ObservedBindingToken != source.BindingToken {
		return nil, library.ErrOnlineUnverified
	}
	// Explicit original access follows authorization, not recursive admission Ignore rules.
	fullPath, currentInfo, err := e.CheckLocationPath(source, position.Path)
	if err != nil {
		return nil, fmt.Errorf("online file changed; synchronize the source: %w", err)
	}
	if !currentInfo.Mode().IsRegular() {
		return nil, library.ErrOnlineConflict
	}
	opened, err := os.Open(fullPath)
	if err != nil {
		return nil, err
	}
	info, err := opened.Stat()
	if err != nil {
		_ = opened.Close()
		return nil, err
	}
	if !os.SameFile(currentInfo, info) || !OnlineFactsMatch(position, info) {
		_ = opened.Close()
		return nil, library.ErrOnlineConflict
	}
	return opened, nil
}

func OnlineFactsMatch(p *library.OnlinePosition, info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Size() == p.Size && uint32(info.Mode()) == p.Mode && info.ModTime().UnixNano() == p.MtimeNS
}

// ResolveOnlineFile returns the validated path and actual Location for exactly the frozen content.
// Callers hold UseOnlineRead across their identity-sensitive operation.
func (e *Executor) ResolveOnlineFile(ctx context.Context, expected *entity.ExpectedFile) (string, int64, error) {
	// A supported transfer requires concrete integrity facts, not a particular signature encoding.
	if expected == nil || expected.FileId <= 0 || len(expected.Signature) == 0 || len(expected.Sha256) != 32 || expected.Size < 0 {
		return "", 0, fmt.Errorf("selected File has incomplete content facts")
	}

	// The one-to-one original binding is authoritative; another File's equal content is not a fallback.
	position, err := e.lib.GetOnlinePosition(ctx, expected.FileId)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", 0, fmt.Errorf("File %d has no online source; automatic Restore is not supported", expected.FileId)
	}
	if err != nil {
		return "", 0, err
	}
	opened, err := e.OpenOnlinePosition(ctx, position, expected)
	if err != nil {
		return "", 0, fmt.Errorf("File %d original is unavailable; synchronize or reconnect: %w", expected.FileId, err)
	}
	filename := opened.Name()
	if err := opened.Close(); err != nil {
		return "", 0, err
	}
	return filename, position.SourceID, nil
}
