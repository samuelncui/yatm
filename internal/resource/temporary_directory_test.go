package resource

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTemporaryCleanupHonorsAgeAndLiveOwners(t *testing.T) {
	// Disposable names carry UTC creation time; durable and unfamiliar entries are excluded.
	work := t.TempDir()
	live, err := NewTemporaryDirectory(work, "live-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, live.Close()) })
	name := filepath.Base(live.Directory)
	require.Greater(t, len(name), len(temporaryTimestamp))
	created, err := time.Parse(temporaryTimestamp, name[:len(temporaryTimestamp)])
	require.NoError(t, err)
	require.WithinDuration(t, time.Now(), created, time.Minute, "directory prefix must record UTC creation time")
	now := time.Now().Add(8 * 24 * time.Hour)
	root := TemporaryRoot(work)
	old := filepath.Join(root, now.Add(-8*24*time.Hour).UTC().Format(temporaryTimestamp)+"-abandoned")
	recent := filepath.Join(root, now.Add(-6*24*time.Hour).UTC().Format(temporaryTimestamp)+"-recent")
	boundary := filepath.Join(root, now.Add(-7*24*time.Hour).UTC().Format(temporaryTimestamp)+"-boundary")
	unknown := filepath.Join(root, "unknown")
	durable := filepath.Join(work, "jobs", "1")
	for _, path := range []string{old, recent, boundary, unknown, durable} {
		require.NoError(t, os.MkdirAll(path, 0700))
		require.NoError(t, os.WriteFile(filepath.Join(path, "keep"), []byte("content"), 0600))
	}
	outside := t.TempDir()
	marker := filepath.Join(outside, "keep")
	require.NoError(t, os.WriteFile(marker, []byte("outside"), 0600))
	link := filepath.Join(root, now.Add(-8*24*time.Hour).UTC().Format(temporaryTimestamp)+"-link")
	require.NoError(t, os.Symlink(outside, link))

	// An old live operation survives while abandoned content is removed recursively.
	require.NoError(t, CleanupTemporary(work, now))
	require.NoDirExists(t, old)
	for _, path := range []string{live.Directory, recent, boundary, unknown, durable} {
		require.DirExists(t, path)
	}
	require.FileExists(t, marker)
	require.FileExists(t, filepath.Join(link, "keep"))
}

func TestTemporaryDBCloseRetainsFailedRemovalForRetry(t *testing.T) {
	// Deny unlinking the owned directory without injecting an artificial filesystem implementation.
	if os.Geteuid() == 0 {
		t.Skip("permission fixture requires an unprivileged user")
	}
	work := t.TempDir()
	stage, err := OpenTemporaryDB(work, "retry-")
	require.NoError(t, err)
	path := stage.Directory
	root := TemporaryRoot(work)
	t.Cleanup(func() {
		require.NoError(t, os.Chmod(root, 0700))
		require.NoError(t, stage.Close())
	})
	require.NoError(t, os.Chmod(root, 0500))

	// A failed removal retains the exact path and owner after the database is closed.
	require.Error(t, stage.Close())
	require.Equal(t, path, stage.Directory)
	require.Nil(t, stage.DB)
	require.NoError(t, os.Chmod(root, 0700))
	require.NoError(t, stage.Close())
	require.Empty(t, stage.Directory)
	require.NoDirExists(t, path)
}

func TestTemporaryCleanupRejectsLinkedRoot(t *testing.T) {
	// A root symlink cannot redirect a startup sweep into another owner's files.
	work, outside := t.TempDir(), t.TempDir()
	require.NoError(t, os.Symlink(outside, TemporaryRoot(work)))
	require.Error(t, CleanupTemporary(work, time.Now()))
	_, err := NewTemporaryDirectory(work, "test-")
	require.Error(t, err)
	_, err = NewTemporaryDirectory("", "test-")
	require.Error(t, err)
}

func TestTemporaryCleanupServiceStops(t *testing.T) {
	// Startup creates the private root and cancellation drains its background owner.
	work := t.TempDir()
	stop, err := StartTemporaryCleanup(context.Background(), work, func(err error) { t.Errorf("cleanup failed: %v", err) })
	require.NoError(t, err)
	require.DirExists(t, TemporaryRoot(work))
	stop()
	stop()
}

func TestTemporaryCleanupServiceSweepsPeriodically(t *testing.T) {
	// Create the leftover after startup so only the periodic pass can remove it.
	work := t.TempDir()
	stop, err := startTemporaryCleanup(context.Background(), work, 10*time.Millisecond, func(err error) { t.Errorf("cleanup failed: %v", err) })
	require.NoError(t, err)
	defer stop()
	stale := filepath.Join(TemporaryRoot(work), time.Now().Add(-8*24*time.Hour).UTC().Format(temporaryTimestamp)+"-leftover")
	require.NoError(t, os.Mkdir(stale, 0700))

	// The recurring owner removes the abandoned directory without another explicit sweep.
	require.Eventually(t, func() bool { _, err := os.Stat(stale); return os.IsNotExist(err) }, 5*time.Second, 10*time.Millisecond)
}

func TestTemporaryCleanupConcurrentOwners(t *testing.T) {
	// Allocation and sweeps overlap while each operation owns an independent directory.
	work := t.TempDir()
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			for range 16 {
				directory, err := NewTemporaryDirectory(work, "concurrent-")
				if err != nil {
					t.Error(err)
					return
				}
				if err := CleanupTemporary(work, time.Now().Add(8*24*time.Hour)); err != nil {
					t.Error(err)
				}
				if _, err := os.Stat(directory.Directory); err != nil {
					t.Error(err)
				}
				if err := directory.Close(); err != nil {
					t.Error(err)
				}
			}
		})
	}
	group.Wait()
}

func TestTemporaryCleanupAfterProcessExit(t *testing.T) {
	// The child exits without Close, leaving no process-local owner in the next service.
	if work := os.Getenv("YATM_TEMPORARY_EXIT_TEST_WORK"); work != "" {
		directory, err := NewTemporaryDirectory(work, "interrupted-")
		require.NoError(t, err)
		stale := filepath.Join(TemporaryRoot(work), time.Now().Add(-8*24*time.Hour).UTC().Format(temporaryTimestamp)+"-interrupted")
		require.NoError(t, os.Rename(directory.Directory, stale))
		require.NoError(t, os.WriteFile(filepath.Join(stale, "state.sqlite"), []byte("unfinished disposable state"), 0600))
		os.Exit(0)
	}
	work := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestTemporaryCleanupAfterProcessExit$")
	command.Env = append(os.Environ(), "YATM_TEMPORARY_EXIT_TEST_WORK="+work)
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
	entries, err := os.ReadDir(TemporaryRoot(work))
	require.NoError(t, err)
	require.Len(t, entries, 1)

	// A fresh service startup removes the expired leftover before admitting requests.
	stop, err := StartTemporaryCleanup(context.Background(), work, func(err error) { t.Errorf("cleanup failed: %v", err) })
	require.NoError(t, err)
	stop()
	entries, err = os.ReadDir(TemporaryRoot(work))
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestTemporaryCleanupStartupWarnsAndRetriesUnremovableLeftover(t *testing.T) {
	// Root preparation does not sweep; failed deletion of one old directory leaves the root usable.
	if os.Geteuid() == 0 {
		t.Skip("permission fixture requires an unprivileged user")
	}
	work := t.TempDir()
	require.NoError(t, PrepareTemporary(work))
	stale := filepath.Join(TemporaryRoot(work), time.Now().Add(-8*24*time.Hour).UTC().Format(temporaryTimestamp)+"-blocked")
	require.NoError(t, os.Mkdir(stale, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(stale, "state.sqlite"), []byte("unfinished"), 0600))
	t.Cleanup(func() { _ = os.Chmod(stale, 0700) })
	require.NoError(t, os.Chmod(stale, 0500))
	require.NoError(t, PrepareTemporary(work))
	require.DirExists(t, stale)

	// Startup reports the stale deletion failure while admitting new temporary owners.
	warnings := make(chan error, 1)
	stop, err := startTemporaryCleanup(context.Background(), work, 10*time.Millisecond, func(err error) {
		select {
		case warnings <- err:
		default:
		}
	})
	require.NoError(t, err)
	defer stop()
	select {
	case warning := <-warnings:
		require.ErrorIs(t, warning, os.ErrPermission)
	default:
		t.Fatal("startup must report the stale deletion failure")
	}
	current, err := NewTemporaryDirectory(work, "admitted-")
	require.NoError(t, err)
	require.NoError(t, current.Close())

	// The existing cadence retries the same leftover after its filesystem permissions recover.
	require.NoError(t, os.Chmod(stale, 0700))
	require.Eventually(t, func() bool { _, err := os.Stat(stale); return os.IsNotExist(err) }, 5*time.Second, 10*time.Millisecond)
}

func TestTemporaryCleanupPagesEntries(t *testing.T) {
	// More leftovers than one read page must all be visited while entries are removed.
	work := t.TempDir()
	require.NoError(t, PrepareTemporary(work))
	prefix := time.Now().Add(-8 * 24 * time.Hour).UTC().Format(temporaryTimestamp)
	for i := range 300 {
		require.NoError(t, os.Mkdir(filepath.Join(TemporaryRoot(work), fmt.Sprintf("%s-leftover-%d", prefix, i)), 0700))
	}

	// A bounded directory scan still completes the whole expiration pass.
	require.NoError(t, CleanupTemporary(work, time.Now()))
	entries, err := os.ReadDir(TemporaryRoot(work))
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestTemporaryCleanupRemovesUnheldKeysRegardlessOfAge(t *testing.T) {
	// A fresh abandoned key is removed at once while live keys and fresh ordinary work remain.
	work := t.TempDir()
	live, err := NewTemporaryDirectory(work, TemporaryKeyPrefix)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, live.Close()) })
	prefix := time.Now().UTC().Format(temporaryTimestamp)
	abandoned := filepath.Join(TemporaryRoot(work), prefix+"-"+TemporaryKeyPrefix+"abandoned")
	recent := filepath.Join(TemporaryRoot(work), prefix+"-script-output-recent")
	for _, path := range []string{abandoned, recent} {
		require.NoError(t, os.Mkdir(path, 0700))
	}
	require.NoError(t, os.WriteFile(filepath.Join(abandoned, "key"), []byte("secret"), 0600))

	// Only the unheld key directory leaves the root.
	require.NoError(t, CleanupTemporary(work, time.Now()))
	require.NoDirExists(t, abandoned)
	require.DirExists(t, recent)
	require.DirExists(t, live.Directory)
}
