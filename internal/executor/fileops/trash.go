package fileops

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/google/uuid"
	"github.com/samuelncui/yatm/internal/executor"
)

const trashIdentity = "YATM Location Trash\n1\n"

var trashInitialization sync.Mutex

func validateTrash(root string) error {
	// An existing directory is accepted only with the exact bounded ownership marker.
	directory := filepath.Join(root, executor.LocationTrashDirectory)
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect Trash failed, %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("Trash is not an owned directory")
	}
	marker := filepath.Join(directory, executor.LocationTrashMarker)
	info, err = os.Lstat(marker)
	if err != nil {
		return fmt.Errorf("Trash ownership is unknown: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() != int64(len(trashIdentity)) {
		return fmt.Errorf("Trash ownership marker is invalid")
	}
	file, err := os.Open(marker)
	if err != nil {
		return fmt.Errorf("open Trash ownership failed, %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(len(trashIdentity)+1)))
	if err != nil {
		return fmt.Errorf("read Trash ownership failed, %w", err)
	}
	if string(data) != trashIdentity {
		return fmt.Errorf("Trash belongs to another application")
	}
	return nil
}

func (s *locationTree) trashTarget(ctx context.Context, item *Item) (string, error) {
	// Validate boundaries before creating any managed directories.
	if executor.IsLocationTrashPath(item.SourcePath) {
		return "", fmt.Errorf("Trash entries cannot be removed")
	}
	if err := s.op.protectedRange(ctx, s.location, executor.LocationTrashDirectory, true); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	directory := filepath.Join(s.location.RootPath, executor.LocationTrashDirectory)
	if err := ensureTrash(s.location.RootPath); err != nil {
		return "", err
	}
	same, err := sameMount(s.location.RootPath, directory)
	if err != nil {
		return "", fmt.Errorf("inspect Trash mount failed, %w", err)
	}
	if !same {
		return "", fmt.Errorf("Trash must be on the Location filesystem")
	}

	// Operation and item identities reserve an exclusive, collision-free output container.
	operationID, err := uuid.Parse(s.config.OperationID)
	if err != nil {
		return "", fmt.Errorf("invalid operation identity, %w", err)
	}
	batch := uuid.NewSHA1(operationID, []byte(strconv.FormatInt(item.ID, 10))).String()
	container := path.Join(executor.LocationTrashDirectory, batch)
	if err := os.Mkdir(filepath.Join(s.location.RootPath, filepath.FromSlash(container)), 0700); err != nil {
		return "", fmt.Errorf("reserve Trash container failed, %w", err)
	}
	return path.Join(container, path.Base(item.SourcePath)), nil
}

func ensureTrash(root string) error {
	// An already owned Trash needs no initialization lock.
	if validateTrash(root) == nil {
		return nil
	}

	// Serialize only first-use creation and validation, including a marker still being written.
	trashInitialization.Lock()
	defer trashInitialization.Unlock()
	directory := filepath.Join(root, executor.LocationTrashDirectory)
	if err := os.Mkdir(directory, 0700); err != nil {
		if !os.IsExist(err) {
			return fmt.Errorf("create Trash failed, %w", err)
		}
	} else {
		// Exclusive creation must never overwrite an unrecognized user's marker.
		file, err := os.OpenFile(filepath.Join(directory, executor.LocationTrashMarker), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return fmt.Errorf("create Trash ownership failed, %w", err)
		}
		_, writeErr := file.WriteString(trashIdentity)
		closeErr := file.Close()
		if writeErr != nil {
			return fmt.Errorf("write Trash ownership failed, %w", writeErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close Trash ownership failed, %w", closeErr)
		}
	}
	return validateTrash(root)
}
