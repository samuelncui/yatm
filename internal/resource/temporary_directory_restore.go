package resource

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

const restoreProbeRecords = ".restore-probes"

type restoreProbeRecord struct {
	Path   string `json:"path"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

// RegisterRestoreProbe records an exact destination-side name probe before the caller
// creates it. Both immediate and expired cleanup retain this record until removal succeeds.
func (d *TemporaryDirectory) RegisterRestoreProbe(path string) (result error) {
	// Only Restore's private directory-only namespace can be registered for external cleanup.
	if d.Directory == "" || !validRestoreProbe(path) {
		return fmt.Errorf("invalid Restore probe cleanup path %q", path)
	}
	device, inode, err := temporaryParentIdentity(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("inspect Restore probe parent failed, %w", err)
	}
	records := filepath.Join(d.Directory, restoreProbeRecords)
	if err := os.MkdirAll(records, 0700); err != nil {
		return fmt.Errorf("create Restore probe records failed, %w", err)
	}

	// A partial record cannot authorize cleanup because its probe has not been created yet.
	file, err := os.CreateTemp(records, "*.pending")
	if err != nil {
		return fmt.Errorf("create Restore probe record failed, %w", err)
	}
	pending := file.Name()
	defer func() {
		if err := os.Remove(pending); err != nil && !os.IsNotExist(err) {
			result = errors.Join(result, err)
		}
	}()
	writeErr := json.NewEncoder(file).Encode(restoreProbeRecord{Path: path, Device: device, Inode: inode})
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return fmt.Errorf("write Restore probe record failed, %w", err)
	}
	if err := os.Rename(pending, strings.TrimSuffix(pending, ".pending")); err != nil {
		return fmt.Errorf("publish Restore probe record failed, %w", err)
	}
	return nil
}

func validRestoreProbe(path string) bool {
	name := filepath.Base(path)
	encoded := strings.TrimSuffix(strings.TrimPrefix(name, ".yatm-restore-"), "-\u00e9")
	_, err := uuid.Parse(encoded)
	return filepath.IsAbs(path) && filepath.Clean(path) == path && err == nil &&
		name == ".yatm-restore-"+encoded+"-\u00e9"
}

func removeTemporaryDirectory(path string) error {
	// External probes must be settled before deleting the only records that identify them.
	if err := removeRestoreProbes(filepath.Join(path, restoreProbeRecords)); err != nil {
		return err
	}
	return os.RemoveAll(path)
}

func removeRestoreProbes(path string) (result error) {
	// Page records independently so one unavailable target cannot strand other destinations.
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect Restore probe records failed, %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("Restore probe records are not a real directory: %q", path)
	}
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open Restore probe records failed, %w", err)
	}
	defer func() { result = errors.Join(result, directory.Close()) }()
	for {
		entries, readErr := directory.ReadDir(128)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return errors.Join(result, fmt.Errorf("list Restore probe records failed, %w", readErr))
		}
		for _, entry := range entries {
			record := filepath.Join(path, entry.Name())
			if !entry.Type().IsRegular() {
				result = errors.Join(result, fmt.Errorf("Restore probe record is not a regular file: %q", record))
				continue
			}
			if strings.HasSuffix(entry.Name(), ".pending") {
				result = errors.Join(result, os.Remove(record))
				continue
			}
			if err := removeRestoreProbe(record); err != nil {
				result = errors.Join(result, err)
				continue
			}
			result = errors.Join(result, os.Remove(record))
		}
		if errors.Is(readErr, io.EOF) {
			return result
		}
	}
}

func removeRestoreProbe(path string) error {
	// Bound disposable record reads; malformed records remain available for diagnosis.
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open Restore probe record failed, %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return fmt.Errorf("read Restore probe record failed, %w", err)
	}
	if len(data) > 64<<10 {
		return fmt.Errorf("Restore probe record is too large: %q", path)
	}
	var record restoreProbeRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return fmt.Errorf("decode Restore probe record failed, %w", err)
	}
	if !validRestoreProbe(record.Path) {
		return fmt.Errorf("invalid Restore probe cleanup path %q", record.Path)
	}

	// An absent or remounted destination is not evidence that its old probe is already gone.
	device, inode, err := temporaryParentIdentity(filepath.Dir(record.Path))
	if err != nil {
		return fmt.Errorf("inspect Restore probe parent failed, path=%q, %w", record.Path, err)
	}
	if device != record.Device || inode != record.Inode {
		return fmt.Errorf("Restore probe parent is unavailable, path=%q", record.Path)
	}
	if err := os.RemoveAll(record.Path); err != nil {
		return fmt.Errorf("remove Restore probe failed, path=%q, %w", record.Path, err)
	}
	return nil
}
