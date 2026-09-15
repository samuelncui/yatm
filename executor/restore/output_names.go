package restore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// outputNames is an attempt-local filesystem namespace, never an output file or durable recovery log.
type outputNames struct {
	runner  *jobRestoreRunner
	index   string
	name    string
	groups  int
	release func()
}

func newOutputNames(ctx context.Context, runner *jobRestoreRunner) (*outputNames, error) {
	// Keep cleanup records on the Job's protected storage, not in an unbounded in-memory directory map.
	work, err := runner.exe.EnsureJobWorkPath(ctx, runner.job.ID)
	if err != nil {
		return nil, err
	}
	index, err := os.MkdirTemp(work, "output-names-")
	if err != nil {
		return nil, err
	}
	prefix := ".yatm-restore-" + uuid.NewString()
	release, err := runner.exe.ProtectTemporaryNames(prefix)
	if err != nil {
		return nil, errors.Join(err, os.Remove(index))
	}
	return &outputNames{runner: runner, index: index, name: prefix + "-\u00e9", release: release}, nil
}

func (n *outputNames) rebuild(ctx context.Context) error {
	// Frozen reservations win before any new item can choose an equivalent spelling on retry.
	var after int64
	for {
		var page []Output
		if err := n.runner.db.WithContext(ctx).Where("item_id > ?", after).Order("item_id").Limit(batchSize).Find(&page).Error; err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		for _, output := range page {
			committed := false
			if n.runner.destination.GetLocationId() != 0 {
				result, err := n.runner.exe.Lib().GetRestoreResult(ctx, n.runner.operationID, output.ItemID)
				if err != nil {
					return err
				}
				committed = result != nil
			}
			claimed, err := n.claim(ctx, output.Path)
			if err != nil {
				return err
			}
			if !claimed && !committed {
				return fmt.Errorf("frozen Restore output paths collide on this filesystem: %q", output.Path)
			}
		}
		after = page[len(page)-1].ItemID
	}
}

func (n *outputNames) claim(ctx context.Context, relative string) (bool, error) {
	// Resolve the actual nearest existing parent, including existing directory aliases and nested mounts.
	if err := ctx.Err(); err != nil {
		return false, err
	}
	full, err := n.runner.exe.RestoreOutputPath(ctx, n.runner.destination, relative)
	if err != nil {
		return false, err
	}
	return n.claimPath(ctx, full)
}

func (n *outputNames) claimPath(ctx context.Context, full string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	parent, suffix := filepath.Dir(full), filepath.Base(full)
	for {
		info, err := os.Lstat(parent)
		if err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return false, fmt.Errorf("Restore output parent is not a real directory: %q", parent)
			}
			break
		}
		if !os.IsNotExist(err) {
			return false, err
		}
		suffix = filepath.Join(filepath.Base(parent), suffix)
		parent = filepath.Dir(parent)
	}
	shadow, err := n.directory(parent)
	if err != nil {
		return false, err
	}
	if err := n.seedOwners(ctx, parent, shadow); err != nil {
		return false, err
	}
	return n.claimRelative(shadow, suffix)
}

func (n *outputNames) claimRelative(shadow, suffix string) (bool, error) {
	// Directories and a private leaf marker reproduce native name lookup without creating regular files.
	parts := strings.Split(suffix, string(filepath.Separator))
	for index, part := range parts {
		if strings.HasPrefix(part, n.name) {
			return false, fmt.Errorf("Restore output overlaps an active name probe")
		}
		shadow = filepath.Join(shadow, part)
		err := os.Mkdir(shadow, 0700)
		if err != nil && !os.IsExist(err) {
			return false, err
		}
		leaf := index == len(parts)-1
		if err == nil && leaf {
			return true, os.Mkdir(filepath.Join(shadow, n.name), 0700)
		}
		info, err := os.Lstat(shadow)
		if err != nil {
			return false, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false, fmt.Errorf("Restore name probe is not a real directory")
		}
		_, markerErr := os.Lstat(filepath.Join(shadow, n.name))
		if markerErr != nil && !os.IsNotExist(markerErr) {
			return false, markerErr
		}
		marked := markerErr == nil
		if leaf && marked {
			return false, nil
		}
		if leaf || marked {
			return false, fmt.Errorf("Restore outputs have a file/directory name conflict: %q", suffix)
		}
	}
	return false, fmt.Errorf("Restore output path is empty")
}

func (n *outputNames) directory(parent string) (string, error) {
	// One random basename under each actual parent naturally shares a namespace across parent aliases.
	shadow := filepath.Join(parent, n.name)
	err := os.Mkdir(shadow, 0700)
	if os.IsExist(err) {
		info, inspectErr := os.Lstat(shadow)
		if inspectErr != nil {
			return "", inspectErr
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("Restore name probe is not a real directory")
		}
		return shadow, nil
	}
	if err != nil {
		return "", err
	}
	n.groups++
	if err := os.WriteFile(filepath.Join(n.index, strconv.Itoa(n.groups)), []byte(shadow), 0600); err != nil {
		return "", errors.Join(err, os.Remove(shadow))
	}

	// Check inherited case/normalization behavior in the actual directories, including network mounts.
	parentRules, err := directoryNameRules(parent, n.name)
	if err != nil {
		return "", err
	}
	child := filepath.Join(shadow, n.name)
	if err := os.Mkdir(child, 0700); err != nil {
		return "", err
	}
	shadowRules, err := directoryNameRules(shadow, n.name)
	if err != nil {
		return "", err
	}
	if err := os.Remove(child); err != nil {
		return "", err
	}
	if parentRules != shadowRules {
		return "", fmt.Errorf("Restore target does not inherit directory name rules: %q", parent)
	}
	return shadow, nil
}

func directoryNameRules(directory, name string) ([2]bool, error) {
	// Probe aliases of an owned directory; actual candidate spellings are never normalized in Go or SQL.
	var result [2]bool
	original, err := os.Lstat(filepath.Join(directory, name))
	if err != nil {
		return result, err
	}
	for index, variant := range []string{
		strings.Replace(name, ".yatm-", ".YATM-", 1),
		strings.Replace(name, "\u00e9", "e\u0301", 1),
	} {
		alternate, err := os.Lstat(filepath.Join(directory, variant))
		if err != nil && !os.IsNotExist(err) {
			return result, err
		}
		result[index] = err == nil && os.SameFile(original, alternate)
	}
	return result, nil
}

func (n *outputNames) close() error {
	// Clean only exact probe directories recorded by this attempt, in bounded pages.
	index, err := os.Open(n.index)
	if err != nil {
		return err
	}
	defer index.Close()
	var cleanupErr error
	for {
		entries, err := index.ReadDir(batchSize)
		if err != nil && err != io.EOF {
			cleanupErr = errors.Join(cleanupErr, err)
			break
		}
		for _, entry := range entries {
			value, readErr := os.ReadFile(filepath.Join(n.index, entry.Name()))
			if readErr != nil {
				cleanupErr = errors.Join(cleanupErr, readErr)
				continue
			}
			target := string(value)
			if !filepath.IsAbs(target) || filepath.Base(target) != n.name {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("invalid Restore probe cleanup record"))
				continue
			}
			cleanupErr = errors.Join(cleanupErr, os.RemoveAll(target))
		}
		if err == io.EOF {
			break
		}
	}
	if cleanupErr != nil {
		return cleanupErr
	}
	// Failed cleanup retains its runtime exclusion; successful attempts leave no probe state behind.
	n.release()
	return os.RemoveAll(n.index)
}
