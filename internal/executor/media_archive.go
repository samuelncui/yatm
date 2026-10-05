package executor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

func allocateMediaPathPrefix(root string, value uint64) (string, error) {
	for {
		name := strconv.FormatUint(value, 36)
		_, err := os.Lstat(filepath.Join(root, name))
		if errors.Is(err, os.ErrNotExist) {
			return name, nil
		}
		if err != nil {
			return "", fmt.Errorf("inspect Media path prefix failed, path=%q, %w", name, err)
		}
		if value == ^uint64(0) {
			return "", fmt.Errorf("allocate Media path prefix failed, timestamp space is exhausted")
		}
		value++
	}
}
