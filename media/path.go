package media

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/samuelncui/yatm/entity"
)

type pathPurpose int

const (
	pathSource pathPurpose = iota
	pathTarget
)

// ResolveSourcePath resolves one existing regular Media file without following symlinks.
func ResolveSourcePath(root, relative string) (string, error) {
	return resolvePath(root, relative, pathSource)
}

// ResolveTargetPath resolves one Media target while rejecting existing symlink components.
func ResolveTargetPath(root, relative string) (string, error) {
	return resolvePath(root, relative, pathTarget)
}

func resolvePath(root, relative string, purpose pathPurpose) (string, error) {
	// Resolve the trusted root and reject a lexical escape before inspecting components.
	if err := entity.ValidateRelativePath(relative); err != nil {
		return "", err
	}
	canonical, err := canonicalDirectory(root)
	if err != nil {
		return "", err
	}
	result := filepath.Join(canonical, filepath.FromSlash(relative))
	local, err := filepath.Rel(canonical, result)
	if err != nil || local == ".." || strings.HasPrefix(local, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("Media path escapes root, path=%q", relative)
	}

	// Walk every existing component so a symlink cannot redirect Media I/O outside the root.
	current := canonical
	parts := strings.Split(filepath.FromSlash(relative), string(filepath.Separator))
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) && purpose == pathTarget {
			return result, nil
		}
		if statErr != nil {
			return "", fmt.Errorf("inspect Media path failed, path=%q, %w", relative, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("Media path contains a symlink, path=%q", relative)
		}
		last := index == len(parts)-1
		if !last && !info.IsDir() {
			return "", fmt.Errorf("Media path parent is not a directory, path=%q", relative)
		}
		if last && purpose == pathSource && !info.Mode().IsRegular() {
			return "", fmt.Errorf("Media source is not a regular file, path=%q", relative)
		}
		if last && purpose == pathTarget && !info.Mode().IsRegular() {
			return "", fmt.Errorf("Media target is not a regular file, path=%q", relative)
		}
	}
	return result, nil
}
