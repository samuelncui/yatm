package executor

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/samuelncui/yatm/entity"
)

const sourceReadBatchSize = 256

// WalkSource streams regular files from one durable Job source specification.
func (e *Executor) WalkSource(
	ctx context.Context,
	source *entity.Source,
	visit func(root, filename string, info os.FileInfo) error,
) error {
	if source == nil {
		return fmt.Errorf("Job source is nil")
	}
	if visit == nil {
		return fmt.Errorf("Job source visitor is nil")
	}

	// Resolve relative source bases through the Executor's configured data root.
	base := strings.TrimSpace(source.Base)
	if !filepath.IsAbs(base) {
		base = filepath.Join(e.paths.Source, base)
	}
	root := filepath.Clean(filepath.Join(base, filepath.FromSlash(path.Join(source.Path...))))
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("stat Job source failed, path=%q, %w", root, err)
	}
	if info.Mode().IsRegular() {
		return visit(root, root, info)
	}
	if !info.IsDir() {
		return nil
	}
	return walkSourceDirectory(ctx, root, root, visit)
}

func walkSourceDirectory(
	ctx context.Context,
	root, directory string,
	visit func(root, filename string, info os.FileInfo) error,
) error {
	// Keep one directory handle and one bounded entry page live at a time.
	handle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open Job source directory failed, path=%q, %w", directory, err)
	}
	defer handle.Close()

	for {
		entries, readErr := handle.ReadDir(sourceReadBatchSize)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			filename := filepath.Join(directory, entry.Name())
			if entry.IsDir() {
				if err := walkSourceDirectory(ctx, root, filename, visit); err != nil {
					return err
				}
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return fmt.Errorf("stat Job source failed, path=%q, %w", filename, err)
			}
			if !info.Mode().IsRegular() {
				continue
			}
			if err := visit(root, filename, info); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("read Job source directory failed, path=%q, %w", directory, readErr)
		}
	}
}
