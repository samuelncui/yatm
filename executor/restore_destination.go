package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/ignore"
	"github.com/samuelncui/yatm/library"
)

func (e *Executor) FreezeRestoreDestination(ctx context.Context, requested *entity.RestoreDestination) (*entity.RestoreDestination, error) {
	// Clients choose a registration and relative directory; they cannot provide the trusted root.
	if requested == nil || requested.LocationId <= 0 {
		return nil, fmt.Errorf("choose a Location")
	}
	location, err := e.lib.GetOnlineSource(ctx, requested.LocationId)
	if err != nil {
		return nil, err
	}
	destination := &entity.RestoreDestination{LocationId: location.ID, RootPath: location.RootPath, ExecutorId: location.ExecutorID,
		Path: requested.Path, BindingToken: location.BindingToken}
	if _, err := e.RestoreOutputPath(ctx, destination, ""); err != nil {
		return nil, err
	}
	return destination, nil
}

// RestoreOutputPath revalidates a frozen binding and rejects symlinks, storage overlap and denied descendants.
func (e *Executor) RestoreOutputPath(ctx context.Context, destination *entity.RestoreDestination, relative string) (string, error) {
	// Configuration changes may invalidate a Job, but never redirect its output.
	if destination == nil {
		return "", fmt.Errorf("Restore destination is missing")
	}
	// A migrated legacy manifest has an explicit frozen root but no registration identity.
	location := &library.Location{RootPath: destination.RootPath, ExecutorID: destination.ExecutorId, RestoreTarget: true}
	if destination.LocationId != 0 {
		var err error
		location, err = e.lib.GetOnlineSource(ctx, destination.LocationId)
		if err != nil {
			return "", err
		}
		if location.Binding != entity.OnlineBinding_CONFIRMED {
			return "", library.ErrOnlineUnverified
		}
		if location.BindingToken == "" || location.BindingToken != destination.BindingToken {
			return "", library.ErrOnlineConflict
		}
	}
	if location.RootPath == "" {
		return "", fmt.Errorf("Restore output root is missing")
	}
	if location.RootPath != destination.RootPath || location.ExecutorID != destination.ExecutorId {
		return "", library.ErrOnlineConflict
	}
	root, err := e.restoreLocationRoot(location, destination.LocationId == 0)
	if err != nil {
		return "", err
	}
	for _, value := range []string{destination.Path, relative} {
		if value != "" {
			if err := entity.ValidateRelativePath(value); err != nil {
				return "", err
			}
		}
	}
	joined := filepath.Join(destination.Path, filepath.FromSlash(relative))
	full := filepath.Join(root, joined)
	if e.isTemporaryResource(full) {
		return "", fmt.Errorf("%w: Restore output overlaps an active temporary resource", ErrAccessExcluded)
	}

	// Mandatory process/archive exclusions cannot be overridden by either user Ignore configuration.
	for _, excluded := range location.RequiredExclusions {
		if joined == excluded || strings.HasPrefix(filepath.ToSlash(joined), excluded+"/") {
			return "", fmt.Errorf("%w: Restore output overlaps protected storage", ErrAccessExcluded)
		}
	}
	allowed := false
	for _, access := range e.AccessRanges() {
		base, err := CanonicalConfiguredPath(access.Root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(base, full)
		if err != nil || !withinOnlinePath(rel) {
			continue
		}
		matcher := ignore.Compile(access.Ignore)
		if !matcher.Match(filepath.ToSlash(rel), relative == "") {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", fmt.Errorf("%w: Restore output is denied by administrator access rules", ErrAccessExcluded)
	}

	// Missing descendants may be created by the transfer, but existing components must be real paths.
	if joined == "." {
		return root, nil
	}
	parts := strings.Split(joined, string(filepath.Separator))
	current := root
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: Restore output traverses a symlink: %q", ErrAccessExcluded, current)
		}
		if index < len(parts)-1 && !info.IsDir() {
			return "", fmt.Errorf("%w: Restore output parent is not a directory: %q", ErrAccessExcluded, current)
		}
		if relative == "" && !info.IsDir() {
			return "", fmt.Errorf("%w: Restore directory is not a directory: %q", ErrAccessExcluded, current)
		}
	}
	return full, nil
}

func (e *Executor) restoreLocationRoot(location *library.Location, legacy bool) (string, error) {
	if !legacy {
		return e.CheckOnlineSource(location)
	}
	// Frozen legacy configuration may name a not-yet-created directory, never an untrusted raw request.
	root := location.RootPath
	if location.ExecutorID != localExecutorID || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", library.ErrOnlineUnverified
	}
	if !e.originalAccess(root)("", true) {
		return "", fmt.Errorf("legacy Restore root is outside administrator access")
	}
	for current := root; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if err == nil && !info.IsDir() {
			return "", fmt.Errorf("legacy Restore root traverses a non-directory or symlink: %q", current)
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	required, err := e.RequiredOnlineExclusions(root)
	if err != nil {
		return "", err
	}
	location.RequiredExclusions = required
	return root, nil
}
