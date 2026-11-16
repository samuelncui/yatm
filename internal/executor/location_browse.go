package executor

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// nameHeap is a max-heap of directory names, so its root is the largest retained name.
type nameHeap []string

func (h nameHeap) Len() int           { return len(h) }
func (h nameHeap) Less(i, j int) bool { return h[i] > h[j] }
func (h nameHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *nameHeap) Push(v any)        { *h = append(*h, v.(string)) }
func (h *nameHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

// BrowseDirectories reads one administrator-authorized directory and returns, in lexical order,
// at most limit+1 accessible child directory names after the given name, without following
// symbolic links. A name beyond limit means another page exists.
func (e *Executor) BrowseDirectories(ctx context.Context, root, after string, limit int) ([]string, error) {
	handle, err := os.Open(root)
	if err != nil {
		return nil, err
	}
	defer handle.Close()

	// Keep only the next bounded lexical page while reading the directory in fixed-size batches:
	// a max-heap retains the smallest limit+1 names.
	names := make(nameHeap, 0, limit+1)
	read := func(n int) ([]os.DirEntry, error) {
		entries, err := handle.ReadDir(n)
		if err != nil && err != io.EOF {
			err = fmt.Errorf("browse directory failed, %w", err)
		}
		return entries, err
	}
	if err := ReadLocationEntries(ctx, read, func(entries []os.DirEntry) error {
		for _, entry := range entries {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || entry.Name() <= after {
				continue
			}
			candidate := filepath.Join(root, entry.Name())
			if _, err := e.LocationRoot(candidate); err != nil {
				if errors.Is(err, ErrAccessExcluded) {
					continue
				}
				return fmt.Errorf("check directory access failed, %w", err)
			}
			if len(names) <= limit {
				heap.Push(&names, entry.Name())
				continue
			}
			if entry.Name() < names[0] {
				names[0] = entry.Name()
				heap.Fix(&names, 0)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}
