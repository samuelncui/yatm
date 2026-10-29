package resource

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func createRestoreProbe(t *testing.T, owner *TemporaryDirectory, parent string, id int) string {
	t.Helper()
	path := filepath.Join(parent, fmt.Sprintf(".yatm-restore-00000000-0000-4000-8000-%012d-é", id))
	require.NoError(t, owner.RegisterRestoreProbe(path))
	require.NoError(t, os.Mkdir(path, 0700))
	return path
}

func TestTemporaryRestoreProbeClosePreservesUnregisteredDirectories(t *testing.T) {
	// Registration does not create the native namespace; its exact record precedes that side effect.
	owner, err := NewTemporaryDirectory(t.TempDir(), "restore-output-names-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	parent := t.TempDir()
	path := filepath.Join(parent, ".yatm-restore-00000000-0000-4000-8000-000000000001-é")
	require.NoError(t, owner.RegisterRestoreProbe(path))
	require.NoDirExists(t, path)
	records, err := os.ReadDir(filepath.Join(owner.Directory, restoreProbeRecords))
	require.NoError(t, err)
	require.Len(t, records, 1)
	data, err := os.ReadFile(filepath.Join(owner.Directory, restoreProbeRecords, records[0].Name()))
	require.NoError(t, err)
	var record restoreProbeRecord
	require.NoError(t, json.Unmarshal(data, &record))
	require.Equal(t, path, record.Path)
	require.NoError(t, os.MkdirAll(filepath.Join(path, "nested", "file.txt"), 0700))

	// Native probes are directory-only, and neighboring user directories are never searched or removed.
	unrelated := filepath.Join(parent, ".yatm-restore-unregistered")
	require.NoError(t, os.Mkdir(unrelated, 0700))
	require.Error(t, owner.RegisterRestoreProbe(unrelated))
	index := owner.Directory
	require.NoError(t, owner.Close())
	require.NoDirExists(t, path)
	require.NoDirExists(t, index)
	require.DirExists(t, unrelated)
}

func TestTemporaryRestoreProbeExpiryAfterProcessExit(t *testing.T) {
	// A new service has no process-local hold for the interrupted allocation.
	if work := os.Getenv("YATM_RESTORE_PROBE_EXIT_TEST_WORK"); work != "" {
		owner, err := NewTemporaryDirectory(work, "restore-output-names-")
		require.NoError(t, err)
		parent := filepath.Join(work, "destination")
		require.NoError(t, os.Mkdir(parent, 0700))
		createRestoreProbe(t, owner, parent, 2)
		stale := filepath.Join(TemporaryRoot(work), time.Now().Add(-8*24*time.Hour).UTC().Format(temporaryTimestamp)+"-restore-output-names-interrupted")
		require.NoError(t, os.Rename(owner.Directory, stale))
		os.Exit(0)
	}
	work := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestTemporaryRestoreProbeExpiryAfterProcessExit$")
	command.Env = append(os.Environ(), "YATM_RESTORE_PROBE_EXIT_TEST_WORK="+work)
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)

	// Startup uses the same external removal path as Close, without an existing Job or runner.
	stop, err := StartTemporaryCleanup(context.Background(), work, func(err error) { t.Errorf("cleanup failed: %v", err) })
	require.NoError(t, err)
	stop()
	entries, err := os.ReadDir(TemporaryRoot(work))
	require.NoError(t, err)
	require.Empty(t, entries)
	entries, err = os.ReadDir(filepath.Join(work, "destination"))
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestTemporaryRestoreProbeActiveOwnerSurvivesExpiry(t *testing.T) {
	// Creation age never overrides a live operation's hold, including its external namespaces.
	work := t.TempDir()
	owner, err := NewTemporaryDirectory(work, "restore-output-names-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	path := createRestoreProbe(t, owner, t.TempDir(), 3)
	require.NoError(t, CleanupTemporary(work, time.Now().Add(8*24*time.Hour)))
	require.DirExists(t, owner.Directory)
	require.DirExists(t, path)
}

func TestTemporaryRestoreProbeUnavailableParentRetainsOnlyFailedRecords(t *testing.T) {
	// An absent or replaced mount parent cannot turn a missing probe into confirmed cleanup.
	work := t.TempDir()
	owner, err := NewTemporaryDirectory(work, "restore-output-names-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	parent := filepath.Join(t.TempDir(), "destination")
	require.NoError(t, os.Mkdir(parent, 0700))
	path := createRestoreProbe(t, owner, parent, 4)
	other := createRestoreProbe(t, owner, t.TempDir(), 5)
	parked := parent + "-offline"
	require.NoError(t, os.Rename(parent, parked))
	index := owner.Directory
	require.Error(t, owner.Close())
	require.NoDirExists(t, other)
	records, err := os.ReadDir(filepath.Join(index, restoreProbeRecords))
	require.NoError(t, err)
	require.Len(t, records, 1)

	// A different parent at the old spelling and its contents remain untouched on the next sweep.
	require.NoError(t, os.Mkdir(parent, 0700))
	require.NoError(t, os.Mkdir(path, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(path, "keep"), []byte("unrelated"), 0600))
	require.ErrorContains(t, CleanupTemporary(work, time.Now().Add(8*24*time.Hour)), "parent is unavailable")
	require.FileExists(t, filepath.Join(path, "keep"))
	require.DirExists(t, index)

	// Restoring the original parent makes the original exact namespace reclaimable.
	require.NoError(t, os.RemoveAll(parent))
	require.NoError(t, os.Rename(parked, parent))
	require.NoError(t, CleanupTemporary(work, time.Now().Add(8*24*time.Hour)))
	require.NoDirExists(t, path)
	require.NoDirExists(t, index)
}

func TestTemporaryRestoreProbeCleanupPagesRecords(t *testing.T) {
	// A complete external-record pass is bounded by directory pages rather than a retained path list.
	owner, err := NewTemporaryDirectory(t.TempDir(), "restore-output-names-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	parent := t.TempDir()
	for i := range 257 {
		createRestoreProbe(t, owner, parent, i)
	}
	require.NoError(t, owner.Close())
	entries, err := os.ReadDir(parent)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestTemporaryRestoreProbeCleanupRejectsMalformedAndLinkedRecords(t *testing.T) {
	// Invalid or linked published records cannot authorize deletion outside the registered namespace.
	owner, err := NewTemporaryDirectory(t.TempDir(), "restore-output-names-")
	require.NoError(t, err)
	parent := t.TempDir()
	path := createRestoreProbe(t, owner, parent, 6)
	records := filepath.Join(owner.Directory, restoreProbeRecords)
	unrelated := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(unrelated, "keep"), []byte("original"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(records, "unpublished.pending"), []byte(unrelated), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(records, "invalid"), []byte(`{"path":"`+unrelated+`"}`), 0600))
	require.NoError(t, os.Symlink(filepath.Join(unrelated, "keep"), filepath.Join(records, "linked")))
	require.Error(t, owner.Close())
	require.NoDirExists(t, path)
	require.FileExists(t, filepath.Join(unrelated, "keep"))
	require.NoFileExists(t, filepath.Join(records, "unpublished.pending"))
	require.FileExists(t, filepath.Join(records, "invalid"))
	require.NoError(t, os.Remove(filepath.Join(records, "invalid")))
	require.NoError(t, os.Remove(filepath.Join(records, "linked")))
	require.NoError(t, owner.Close())
}
