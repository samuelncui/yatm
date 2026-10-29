package resource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	temporaryTimestamp = "20060102T150405.000000000Z"
	temporaryRetention = 7 * 24 * time.Hour
	temporarySweep     = time.Hour

	// TemporaryKeyPrefix names transient Tape key allocations; cleanup removes them whenever unheld.
	TemporaryKeyPrefix = "tape-key-"
)

var temporaryOwners = struct {
	sync.Mutex
	paths map[string]struct{}
}{paths: make(map[string]struct{})}

// TemporaryDirectory owns disposable files under Work. Close removes them immediately;
// failed removal retains Directory for a later Close or the age-based cleanup.
type TemporaryDirectory struct {
	Directory string
}

// TemporaryRoot returns the sole root for disposable runtime files.
func TemporaryRoot(work string) string { return filepath.Join(work, "temporary") }

func prepareTemporaryRoot(work string) (string, error) {
	// An explicit Work directory keeps disposable data out of shared system storage.
	if work == "" {
		return "", errors.New("temporary storage requires a configured Work directory")
	}
	root, err := filepath.Abs(TemporaryRoot(work))
	if err != nil {
		return "", fmt.Errorf("resolve temporary root failed, %w", err)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", fmt.Errorf("create temporary root failed, %w", err)
	}

	// Refuse a redirected root before any recursive deletion can use it.
	info, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("inspect temporary root failed, %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("temporary root is not a directory: %q", root)
	}
	return root, nil
}

// NewTemporaryDirectory reserves one UTC-prefixed directory until Close finishes its use.
func NewTemporaryDirectory(work, prefix string) (*TemporaryDirectory, error) {
	// Publish each unique directory with its hold before a sweep can inspect ownership.
	temporaryOwners.Lock()
	defer temporaryOwners.Unlock()
	root, err := prepareTemporaryRoot(work)
	if err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp(root, time.Now().UTC().Format(temporaryTimestamp)+"-"+prefix)
	if err != nil {
		return nil, fmt.Errorf("create temporary directory failed, %w", err)
	}
	temporaryOwners.paths[directory] = struct{}{}
	return &TemporaryDirectory{Directory: directory}, nil
}

func (d *TemporaryDirectory) Close() error {
	// Keep the live hold through removal, then make failed cleanup available to the age-based sweep.
	if d.Directory == "" {
		return nil
	}
	path := d.Directory
	defer func() {
		temporaryOwners.Lock()
		delete(temporaryOwners.paths, path)
		temporaryOwners.Unlock()
	}()
	if err := removeTemporaryDirectory(d.Directory); err != nil {
		return fmt.Errorf("remove temporary directory %q failed, %w", d.Directory, err)
	}
	d.Directory = ""
	return nil
}

// PrepareTemporary validates and creates the disposable root without removing leftovers.
func PrepareTemporary(work string) error {
	_, err := prepareTemporaryRoot(work)
	return err
}

// CleanupTemporary removes abandoned UTC-prefixed directories older than seven days and every
// unheld Tape key directory regardless of age. Durable Job bundles, recovery material and LTFS
// mount points live outside this root.
func CleanupTemporary(work string, now time.Time) (result error) {
	// Page a private root without holding up unrelated temporary allocations.
	root, err := prepareTemporaryRoot(work)
	if err != nil {
		return err
	}
	directory, err := os.Open(root)
	if err != nil {
		return fmt.Errorf("open temporary root failed, %w", err)
	}
	defer func() { result = errors.Join(result, directory.Close()) }()
	for {
		entries, readErr := directory.ReadDir(128)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return errors.Join(result, fmt.Errorf("list temporary root failed, %w", readErr))
		}

		// The name records creation time; modified contents cannot renew retention.
		// An unheld key directory is never in use, so its secret is not retained for the age limit.
		for _, entry := range entries {
			name := entry.Name()
			if !entry.IsDir() || len(name) <= len(temporaryTimestamp) || name[len(temporaryTimestamp)] != '-' {
				continue
			}
			created, err := time.Parse(temporaryTimestamp, name[:len(temporaryTimestamp)])
			if err != nil {
				continue
			}
			key := strings.HasPrefix(name[len(temporaryTimestamp)+1:], TemporaryKeyPrefix)
			if !key && !created.Before(now.Add(-temporaryRetention)) {
				continue
			}
			path := filepath.Join(root, name)
			temporaryOwners.Lock()
			_, held := temporaryOwners.paths[path]
			temporaryOwners.Unlock()
			if held {
				continue
			}

			// Unique allocation never adopts an existing expired directory after this decision.
			if err := removeTemporaryDirectory(path); err != nil {
				result = errors.Join(result, fmt.Errorf("remove stale temporary directory %q failed, %w", path, err))
			}
		}
		if errors.Is(readErr, io.EOF) {
			return result
		}
	}
}

// StartTemporaryCleanup sweeps at startup and hourly until stopped. The caller stops it
// after request and Job owners have drained; background failures are reported for diagnosis.
func StartTemporaryCleanup(ctx context.Context, work string, report func(error)) (func(), error) {
	return startTemporaryCleanup(ctx, work, temporarySweep, report)
}

func startTemporaryCleanup(ctx context.Context, work string, interval time.Duration, report func(error)) (func(), error) {
	// A usable root is required; an individual leftover must not block service startup.
	if err := PrepareTemporary(work); err != nil {
		return nil, err
	}
	if err := CleanupTemporary(work, time.Now()); err != nil {
		report(err)
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if err := CleanupTemporary(work, now); err != nil {
					report(err)
				}
			}
		}
	}()
	return func() { cancel(); <-done }, nil
}
