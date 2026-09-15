package fileops

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
)

func (r *operation) currentLocation(ctx context.Context, config *config) (*library.Location, error) {
	// Numeric IDs cannot retarget an operation after import or configuration changes.
	location, err := r.exe.Lib().GetOnlineSource(ctx, config.LocationID)
	if err != nil {
		return nil, err
	}
	if location.RootPath != config.RootPath || location.BindingToken != config.BindingToken || location.Binding != entity.OnlineBinding_CONFIRMED {
		return nil, library.ErrOnlineConflict
	}
	if _, _, err := r.exe.CheckLocationPath(location, ""); err != nil {
		return nil, err
	}
	return location, nil
}

func within(value, root string) bool { return value == root || strings.HasPrefix(value, root+"/") }

func factsMatch(facts *entity.LocationFileFacts, info os.FileInfo, directoryChanges bool) bool {
	actual := executor.LocationFacts(info)
	if facts == nil || facts.Identity == "" || facts.Identity != actual.Identity || facts.Mode != actual.Mode {
		return false
	}
	if directoryChanges && info.IsDir() {
		return true
	}
	return facts.Size == actual.Size && facts.MtimeNs == actual.MtimeNs
}

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
	// Mandatory exclusions are access protection, independent of the Location's Ignore rules.
	required, err := r.exe.RequiredOnlineExclusions(location.RootPath)
	if err != nil {
		return err
	}
	for _, excluded := range required {
		if within(relative, excluded) || directory && within(excluded, relative) {
			return fmt.Errorf("operation overlaps a protected YATM resource: %q", relative)
		}
	}
	full := filepath.Join(location.RootPath, filepath.FromSlash(relative))
	var after int64
	for {
		locations, more, err := r.exe.Lib().ListOnlineSources(ctx, after, batchSize, nil)
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

func (r *operation) checkTarget(ctx context.Context, location *library.Location, relative string) error {
	// Target names are frozen and exclusive; no overwrite, merge or automatic suffix is permitted.
	if err := entity.ValidateRelativePath(relative); err != nil {
		return err
	}
	if err := r.protectedRange(ctx, location, relative, true); err != nil {
		return err
	}
	parent := path.Dir(relative)
	if parent == "." {
		parent = ""
	}
	_, info, err := r.exe.CheckLocationPath(location, parent)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("target parent is not a directory")
	}
	same, err := sameMount(location.RootPath, filepath.Join(location.RootPath, filepath.FromSlash(parent)))
	if err != nil {
		return fmt.Errorf("check target mount boundary failed, %w", err)
	}
	if !same {
		return fmt.Errorf("target crosses a filesystem boundary")
	}
	if _, err := os.Lstat(filepath.Join(location.RootPath, filepath.FromSlash(relative))); !os.IsNotExist(err) {
		if err != nil {
			return err
		}
		return fmt.Errorf("target already exists: %q", relative)
	}
	return nil
}
