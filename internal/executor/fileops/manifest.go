package fileops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/samuelncui/yatm/internal/library"
)

func (r *operation) currentLocation(ctx context.Context, config *config) (*library.Location, error) {
	// Numeric IDs cannot retarget an operation after import or configuration changes.
	location, err := r.exe.Lib().GetLocation(ctx, config.LocationID)
	if err != nil {
		return nil, err
	}
	if location.RootPath != config.RootPath {
		return nil, library.ErrLocationConflict
	}
	if _, _, err := r.exe.CheckLocationPath(location, ""); err != nil {
		return nil, err
	}
	return location, nil
}

func within(value, root string) bool { return value == root || strings.HasPrefix(value, root+"/") }

// SameMount checks the native mount identity without following a leaf symlink.
func SameMount(root, filename string) (bool, error) { return sameMount(root, filename) }

func (r *operation) protected(ctx context.Context, location *library.Location, relative string, info os.FileInfo) error {
	// A physical directory operation must not cross mount or registered-root responsibilities.
	if relative == "" {
		return fmt.Errorf("Location root is protected")
	}
	filename := filepath.Join(location.RootPath, filepath.FromSlash(relative))
	if info.Mode()&os.ModeSymlink != 0 {
		filename = filepath.Dir(filename)
	}
	same, err := sameMount(location.RootPath, filename)
	if err != nil {
		return fmt.Errorf("check operation mount boundary failed, %w", err)
	}
	if !same {
		return fmt.Errorf("operation cannot cross a filesystem boundary: %q", relative)
	}
	return r.protectedRange(ctx, location, relative, info.IsDir())
}

func (r *operation) protectedRange(ctx context.Context, location *library.Location, relative string, directory bool) error {
	full := filepath.Join(location.RootPath, filepath.FromSlash(relative))
	var after int64
	for {
		locations, more, err := r.exe.Lib().ListLocations(ctx, library.LocationListFilter{AfterID: after, Limit: batchSize})
		if err != nil {
			return err
		}
		for _, other := range locations {
			after = other.ID
			if other.ID == location.ID || other.ExecutorID != location.ExecutorID {
				continue
			}
			if within(filepath.ToSlash(full), filepath.ToSlash(other.RootPath)) || directory && within(filepath.ToSlash(other.RootPath), filepath.ToSlash(full)) {
				return fmt.Errorf("operation overlaps registered Location %q", other.Name)
			}
		}
		if !more {
			return nil
		}
	}
}
