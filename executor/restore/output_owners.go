package restore

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func (n *outputNames) seedOwners(ctx context.Context, parent, shadow string) error {
	// Probe only a real target parent. Unrelated original directories need neither write nor read access.
	if n.runner.destination.GetLocationId() == 0 {
		return nil
	}
	marker := filepath.Join(shadow, n.name+"-owners")
	if _, err := os.Lstat(marker); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	root := n.runner.destination.RootPath
	relative, err := filepath.Rel(root, parent)
	if err != nil {
		return err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("Restore probe parent is outside its Location")
	}
	var components []string
	if relative != "." {
		components = strings.Split(relative, string(filepath.Separator))
	}
	err = n.catalogParents(ctx, root, components, "", func(prefix string) error {
		// The path index seeks this subtree; at most one extra bounded page crosses its end.
		after := prefix
		for {
			page, err := n.runner.exe.Lib().OnlineFilesPage(ctx, n.runner.destination.LocationId, after, batchSize)
			if err != nil {
				return err
			}
			if len(page) == 0 {
				return nil
			}
			for _, original := range page {
				if !strings.HasPrefix(original.Path, prefix) {
					return nil
				}
				if _, err := n.claimRelative(shadow, filepath.FromSlash(strings.TrimPrefix(original.Path, prefix))); err != nil {
					return err
				}
				after = original.Path
			}
		}
	})
	if err != nil {
		return err
	}
	return os.Mkdir(marker, 0700)
}

func (n *outputNames) catalogParents(ctx context.Context, actual string, components []string, cached string, yield func(string) error) error {
	// Match cached ancestor names using native directory lookup, never global case/Unicode folding.
	if len(components) == 0 {
		return yield(cached)
	}
	wanted := components[0]
	after := ""
	var revision int64
	for {
		page, currentRevision, more, err := n.runner.exe.Lib().ListOnlinePositions(ctx, n.runner.destination.LocationId, cached, after, revision, batchSize)
		if err != nil {
			return err
		}
		revision = currentRevision
		for _, entry := range page {
			name := path.Base(strings.TrimSuffix(entry.Path, "/"))
			same := name == wanted
			if !same {
				candidate, err := os.Lstat(filepath.Join(actual, name))
				if err != nil && !os.IsNotExist(err) {
					return err
				}
				if err == nil && candidate.IsDir() {
					target, err := os.Lstat(filepath.Join(actual, wanted))
					if err != nil {
						return err
					}
					same = os.SameFile(candidate, target)
				}
			}
			if !same {
				continue
			}
			if !entry.IsDir {
				return fmt.Errorf("Restore target ancestor is already an indexed file: %q", entry.Path)
			}
			if err := n.catalogParents(ctx, filepath.Join(actual, wanted), components[1:], entry.Path, yield); err != nil {
				return err
			}
		}
		if !more {
			return nil
		}
		after = page[len(page)-1].Path
	}
}
