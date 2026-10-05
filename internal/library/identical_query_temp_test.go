package library

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestPrepareIdenticalTempCleansOnlyOwnedResults(t *testing.T) {
	// Simulate an interrupted prior run and a legacy result in the shared system temp directory.
	work := t.TempDir()
	root := filepath.Join(work, identicalTempName)
	require.NoError(t, os.Mkdir(root, 0700))
	for _, name := range []string{"yatm-identical-stale", "yatm-identical-component-stale"} {
		path := filepath.Join(root, name)
		require.NoError(t, os.Mkdir(path, 0700))
		require.NoError(t, os.WriteFile(filepath.Join(path, "groups.sqlite"), []byte("stale"), 0600))
	}
	unrelated := filepath.Join(root, "unrelated")
	require.NoError(t, os.Mkdir(unrelated, 0700))
	outside := t.TempDir()
	marker := filepath.Join(outside, "keep")
	require.NoError(t, os.WriteFile(marker, []byte("owned elsewhere"), 0600))
	link := filepath.Join(root, "yatm-identical-linked")
	require.NoError(t, os.Symlink(outside, link))
	legacy, err := os.MkdirTemp("", "yatm-identical-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(legacy)) })
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "groups.sqlite"), []byte("legacy"), 0600))

	// Startup removes only its old result names, including a link without following it.
	lib := &Library{}
	release, err := lib.PrepareIdenticalTemp(work)
	require.NoError(t, err)
	defer func() { require.NoError(t, release()) }()
	for _, name := range []string{"yatm-identical-stale", "yatm-identical-component-stale"} {
		require.NoDirExists(t, filepath.Join(root, name))
	}
	require.NoFileExists(t, link)
	require.FileExists(t, marker)
	require.DirExists(t, unrelated)
	require.FileExists(t, filepath.Join(legacy, "groups.sqlite"))
}

func TestPrepareIdenticalTempLockProtectsLiveResults(t *testing.T) {
	// A second process using the same Work directory cannot sweep a live result.
	if work := os.Getenv("YATM_IDENTICAL_TEMP_TEST_WORK"); work != "" {
		lib := &Library{}
		release, err := lib.PrepareIdenticalTemp(work)
		require.NoError(t, err)
		defer release()
		_, err = lib.identicalDirectory("yatm-identical-")
		require.NoError(t, err)
		fmt.Println("ready")
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}

	// Hold the lock in a subprocess so SIGKILL also tests automatic lock release.
	work := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPrepareIdenticalTempLockProtectsLiveResults$")
	cmd.Env = append(os.Environ(), "YATM_IDENTICAL_TEMP_TEST_WORK="+work)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = stdin.Close()
	})
	ready, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "ready", strings.TrimSpace(ready))
	entries, err := os.ReadDir(filepath.Join(work, identicalTempName))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	live := filepath.Join(work, identicalTempName, entries[0].Name())
	_, err = (&Library{}).PrepareIdenticalTemp(work)
	require.Error(t, err)
	require.DirExists(t, live)

	// The next startup removes the result left by the killed process.
	require.NoError(t, cmd.Process.Kill())
	_ = cmd.Wait()
	release, err := (&Library{}).PrepareIdenticalTemp(work)
	require.NoError(t, err)
	defer func() { require.NoError(t, release()) }()
	require.NoDirExists(t, live)
}

func TestPrepareIdenticalTempRejectsLinkedRoot(t *testing.T) {
	// A linked scratch root must not redirect startup deletion outside Work.
	work := t.TempDir()
	outside := t.TempDir()
	marker := filepath.Join(outside, "yatm-identical-foreign")
	require.NoError(t, os.Mkdir(marker, 0700))
	require.NoError(t, os.Symlink(outside, filepath.Join(work, identicalTempName)))
	_, err := (&Library{}).PrepareIdenticalTemp(work)
	require.Error(t, err)
	require.DirExists(t, marker)
}

func TestIdenticalSnapshotsUsePreparedTemp(t *testing.T) {
	// Both complete Find and targeted validation use the service-owned scratch root.
	db, lib := newTestLibrary(t)
	file := &File{Name: "one", Kind: entity.FileKind_FILE_KIND_REGULAR}
	createFileRows(t, db, file)
	require.NoError(t, db.Create(&FileVersion{FileID: file.ID, Signature: []byte("signature")}).Error)
	release, err := lib.PrepareIdenticalTemp(t.TempDir())
	require.NoError(t, err)
	defer func() { require.NoError(t, release()) }()
	root := lib.identicalTempRoot
	scope := IdenticalScope{Source: IdenticalLibrary}
	for _, open := range []func() (*IdenticalSnapshot, error){
		func() (*IdenticalSnapshot, error) { return lib.OpenIdenticalSnapshot(context.Background(), scope) },
		func() (*IdenticalSnapshot, error) {
			return lib.OpenIdenticalComponent(context.Background(), scope, file.ID)
		},
	} {
		snapshot, err := open()
		require.NoError(t, err)
		require.Equal(t, root, filepath.Dir(snapshot.directory))
		require.DirExists(t, snapshot.directory)
		require.NoError(t, snapshot.Close())
		require.NoDirExists(t, snapshot.directory)
	}
}
