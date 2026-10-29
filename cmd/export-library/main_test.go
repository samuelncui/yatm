package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestExportLibraryProcess(t *testing.T) {
	if os.Getenv("YATM_EXPORT_TEST_PROCESS") != "1" {
		return
	}
	if fifo := os.Getenv("YATM_EXPORT_TEST_SIGNAL_FIFO"); fifo != "" {
		// Report only signal readiness; the real named-output path then blocks waiting for a FIFO reader.
		ctx, stop := exportContext()
		defer stop()
		go func() {
			<-ctx.Done()
			fmt.Fprintln(os.Stdout, "cancelled")
		}()
		fmt.Fprintln(os.Stdout, "ready")
		err := writeLibraryExport(ctx, fifo, func(io.Writer) error { return nil })
		t.Fatalf("FIFO without a reader unexpectedly returned: %v", err)
	}

	// All other process fixtures invoke the real CLI parser and command entry point.
	os.Args = append([]string{os.Args[0]}, flag.Args()...)
	main()
	os.Exit(0)
}

func TestExportLibrarySecondSignalTerminatesBlockedFIFO(t *testing.T) {
	// A real FIFO with no reader keeps the native open blocked after cooperative context cancellation.
	mkfifo, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skip("POSIX FIFO creation is unavailable")
	}
	fifo := filepath.Join(t.TempDir(), "export.fifo")
	result, err := exec.Command(mkfifo, fifo).CombinedOutput()
	require.NoError(t, err, "%s", result)
	command := exportCommand(t)
	command.Env = append(command.Env, "YATM_EXPORT_TEST_SIGNAL_FIFO="+fifo)
	stdout, err := command.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, command.Start())
	finished := make(chan error, 1)
	go func() { finished <- command.Wait() }()
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = command.Process.Kill()
			<-finished
		}
	})

	// Readiness guarantees registration precedes the first signal; cancellation does not imply syscall exit.
	reader := bufio.NewReader(stdout)
	await := func(want string) {
		line := make(chan string, 1)
		go func() { value, _ := reader.ReadString('\n'); line <- value }()
		select {
		case value := <-line:
			require.Equal(t, want+"\n", value)
		case <-time.After(3 * time.Second):
			t.Fatalf("child did not report %q", want)
		}
	}
	await("ready")
	require.NoError(t, command.Process.Signal(syscall.SIGTERM))
	await("cancelled")
	select {
	case err := <-finished:
		waited = true
		t.Fatalf("blocked output exited before the second signal: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	// A subsequent signal must retain its default exit action even though the output never became ready.
	require.NoError(t, command.Process.Signal(os.Interrupt))
	select {
	case err := <-finished:
		waited = true
		var exit *exec.ExitError
		require.ErrorAs(t, err, &exit)
		status := command.ProcessState.Sys().(syscall.WaitStatus)
		require.True(t, status.Signaled())
		require.Equal(t, syscall.SIGINT, status.Signal())
	case <-time.After(3 * time.Second):
		t.Fatal("second signal was swallowed while FIFO open remained blocked")
	}
}

func exportCommand(t *testing.T, arguments ...string) *exec.Cmd {
	t.Helper()
	// Reuse the compiled test executable to exercise the real flags and main without a nested build.
	binary, err := os.Executable()
	require.NoError(t, err)
	command := exec.Command(binary, append([]string{"-test.run=^TestExportLibraryProcess$", "--"}, arguments...)...)
	command.Env = append(os.Environ(), "YATM_EXPORT_TEST_PROCESS=1")
	return command
}

func TestExportLibrarySelectionsRoundTrip(t *testing.T) {
	// Keep the command's database, output and log files inside one isolated fixture.
	root := t.TempDir()
	t.Chdir(root)
	ctx := context.Background()
	database := filepath.Join(root, "library.db")
	configuration := filepath.Join(root, "config.yaml")
	require.NoError(t, os.WriteFile(configuration, []byte(fmt.Sprintf(
		"database:\n  dialect: sqlite\n  dsn: %q\npaths:\n  work: %q\n", database, root,
	)), 0600))
	db, err := resource.OpenSQLite(database)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	source := library.New(db)
	require.NoError(t, source.AutoMigrate())
	file, err := source.MkdirAll(ctx, library.Root.ID, "documents", 0755)
	require.NoError(t, err)

	// A Media export must preserve both Tape and Volume identities and their Positions.
	media := []*library.Media{
		{ID: 7, Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "ABC007",
			Profile: (&entity.TapeMediaProfile{Format: library.TapeFormatLTFSV1}).Pack()},
		{ID: 8, Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: "11111111-1111-1111-1111-111111111111",
			Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()},
	}
	require.NoError(t, db.Create(&media).Error)
	for _, medium := range media {
		require.NoError(t, db.Create(&library.Position{
			ID: medium.ID, MediaID: medium.ID, Path: "saved.txt", Mode: 0644, Size: 7,
		}).Error)
	}

	// Use the registered default directly so a stale flag default cannot pass through an explicit override.
	for _, test := range []struct {
		name     string
		types    string
		entities []string
	}{
		{"default", *typesOpt, []string{"media", "file", "file_version", "file_version_archive", "position"}},
		{"file only", "file", []string{"file", "file_version", "file_version_archive"}},
		{"inventory only", " media , POSITION ", []string{"media", "position"}},
		{"duplicates", "file,media,position,FILE,media", []string{"media", "file", "file_version", "file_version_archive", "position"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Invoke the actual command, omitting --types to verify its registered default.
			output := filepath.Join(t.TempDir(), "export.jsonl")
			arguments := []string{"-config", configuration, "-output", output}
			if test.name != "default" {
				arguments = append(arguments, "-types", test.types)
			}
			result, err := exportCommand(t, arguments...).CombinedOutput()
			require.NoError(t, err, "%s", result)
			data, err := os.ReadFile(output)
			require.NoError(t, err)
			var header struct {
				Entities []string `json:"entities"`
			}
			require.NoError(t, json.NewDecoder(bytes.NewReader(data)).Decode(&header))
			require.Equal(t, test.entities, header.Entities)

			// Import through the supported reader and verify selected data retains its identities.
			targetDB, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "restored.db"))
			require.NoError(t, err)
			targetSQL, err := targetDB.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, targetSQL.Close()) })
			target := library.New(targetDB)
			require.NoError(t, target.AutoMigrate())
			require.NoError(t, target.Import(ctx, bytes.NewReader(data), false))
			if test.name != "inventory only" {
				restored, err := target.GetFile(ctx, file.ID)
				require.NoError(t, err)
				require.Equal(t, file.Name, restored.Name)
			}
			var count int64
			require.NoError(t, targetDB.Model(library.ModelMedia).Count(&count).Error)
			if test.name == "file only" {
				require.Zero(t, count)
				require.NoError(t, targetDB.Model(library.ModelPosition).Count(&count).Error)
				require.Zero(t, count)
				return
			}
			require.Equal(t, int64(len(media)), count)
			for _, medium := range media {
				restored, err := target.GetMedia(ctx, medium.ID)
				require.NoError(t, err)
				require.Equal(t, medium.Kind, restored.Kind)
				require.Equal(t, medium.Identity, restored.Identity)
				require.True(t, proto.Equal(medium.Profile, restored.Profile))
				positions, err := target.ListPositions(ctx, medium.ID, "")
				require.NoError(t, err)
				require.Len(t, positions, 1)
				require.Equal(t, medium.ID, positions[0].ID)
				require.Equal(t, "saved.txt", positions[0].Path)
			}
		})
	}
}

func TestExportLibraryRejectsInvalidTypesBeforeOpeningResources(t *testing.T) {
	for _, types := range []string{"unknown", "file,tape", "", ",file", "file,", "file,,media", "file, ,media", "UNSPECIFIED"} {
		t.Run(types, func(t *testing.T) {
			// A valid configuration points to absent files, so any premature database/output open is visible.
			root := t.TempDir()
			database, output := filepath.Join(root, "library.db"), filepath.Join(root, "export.jsonl")
			configuration := filepath.Join(root, "config.yaml")
			require.NoError(t, os.WriteFile(configuration, []byte(fmt.Sprintf(
				"database:\n  dialect: sqlite\n  dsn: %q\npaths:\n  work: %q\n", database, root,
			)), 0600))
			if types == "unknown" {
				configuration = filepath.Join(root, "missing-config.yaml")
			}
			command := exportCommand(t, "-config", configuration, "-types", types, "-output", output)
			command.Dir = root
			result, err := command.CombinedOutput()
			require.Error(t, err)
			require.Contains(t, string(result), "invalid export type")

			// Rejection must not initialize a Catalog, create an output, or start the rotating logger.
			entries, err := os.ReadDir(root)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			require.Equal(t, "config.yaml", entries[0].Name())
		})
	}
}

func TestExportLibraryStdoutRemainsStreaming(t *testing.T) {
	// The default output flag writes JSONL to stdout without allocating a named-file staging object.
	root := t.TempDir()
	configuration := filepath.Join(root, "config.yaml")
	require.NoError(t, os.WriteFile(configuration, []byte(fmt.Sprintf(
		"database:\n  dialect: sqlite\n  dsn: %q\npaths:\n  work: %q\n", filepath.Join(root, "library.db"), root,
	)), 0600))
	command := exportCommand(t, "-config", configuration)
	command.Dir = root
	data, err := command.Output()
	require.NoError(t, err)
	var header struct {
		Format string `json:"format"`
	}
	require.NoError(t, json.NewDecoder(bytes.NewReader(data)).Decode(&header))
	require.Equal(t, "yatm-library-backup", header.Format)
	pending, err := filepath.Glob(filepath.Join(root, ".yatm-export-*"))
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestExportLibraryFailurePreservesPreviousBackup(t *testing.T) {
	for _, name := range []string{"dependent selection", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			// Retain a known backup before taking either the CLI validation or canceled-operation path.
			root := t.TempDir()
			t.Chdir(root)
			configuration, output := filepath.Join(root, "config.yaml"), filepath.Join(root, "export.jsonl")
			require.NoError(t, os.WriteFile(configuration, []byte(fmt.Sprintf(
				"database:\n  dialect: sqlite\n  dsn: %q\npaths:\n  work: %q\n", filepath.Join(root, "library.db"), root,
			)), 0600))
			require.NoError(t, os.WriteFile(output, []byte("previous backup"), 0640))
			if name == "dependent selection" {
				result, err := exportCommand(t, "-config", configuration, "-types", "position", "-output", output).CombinedOutput()
				require.Error(t, err)
				require.Contains(t, string(result), "Position import requires the Media group")
			} else {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				require.ErrorIs(t, exportLibrary(ctx, configuration, "file", output), context.Canceled)
			}

			// An incomplete snapshot cannot truncate the previous output or retain a staging file.
			data, err := os.ReadFile(output)
			require.NoError(t, err)
			require.Equal(t, "previous backup", string(data))
			pending, err := filepath.Glob(filepath.Join(root, ".yatm-export-*"))
			require.NoError(t, err)
			require.Empty(t, pending)
		})
	}
}

func TestWriteLibraryExportPublishesOnlySettledFiles(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			// Compare newly created mode with os.Create under this process's actual umask.
			root := t.TempDir()
			output, reference := filepath.Join(root, "export.jsonl"), filepath.Join(root, "mode-reference")
			require.NoError(t, os.WriteFile(reference, nil, 0666))
			mode, err := os.Stat(reference)
			require.NoError(t, err)
			wantMode := mode.Mode().Perm()
			if existing {
				require.NoError(t, os.WriteFile(output, []byte("previous backup"), 0600))
				require.NoError(t, os.Chmod(output, 0640))
				wantMode = 0640
			}

			// No partial bytes become visible through the destination while streaming is in progress.
			err = writeLibraryExport(context.Background(), output, func(writer io.Writer) error {
				_, err := io.WriteString(writer, "complete backup")
				require.NoError(t, err)
				before, err := os.ReadFile(output)
				if existing {
					require.NoError(t, err)
					require.Equal(t, "previous backup", string(before))
				} else {
					require.ErrorIs(t, err, os.ErrNotExist)
				}
				return nil
			})
			require.NoError(t, err)
			data, err := os.ReadFile(output)
			require.NoError(t, err)
			require.Equal(t, "complete backup", string(data))
			mode, err = os.Stat(output)
			require.NoError(t, err)
			require.Equal(t, wantMode, mode.Mode().Perm())
			pending, err := filepath.Glob(filepath.Join(root, ".yatm-export-*"))
			require.NoError(t, err)
			require.Empty(t, pending)
		})
	}
}

func TestWriteLibraryExportFailuresPreservePreviousBackup(t *testing.T) {
	for _, name := range []string{"export", "cancel", "write", "sync and close"} {
		t.Run(name, func(t *testing.T) {
			// Inject failures through the real staging file; no replacement I/O implementation is required.
			root := t.TempDir()
			output := filepath.Join(root, "export.jsonl")
			require.NoError(t, os.WriteFile(output, []byte("previous backup"), 0640))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := errors.New("export stopped")
			err := writeLibraryExport(ctx, output, func(writer io.Writer) error {
				_, err := io.WriteString(writer, "partial backup")
				require.NoError(t, err)
				switch name {
				case "export":
					return failure
				case "cancel":
					cancel()
					return nil
				default:
					require.NoError(t, writer.(*os.File).Close())
					if name == "write" {
						_, err := io.WriteString(writer, "more bytes")
						return err
					}
					return nil
				}
			})
			require.Error(t, err)
			if name == "sync and close" {
				require.ErrorContains(t, err, "sync export output failed")
				require.ErrorContains(t, err, "close export output failed")
			}
			if name == "cancel" {
				require.ErrorIs(t, err, context.Canceled)
			}

			// Every failure leaves the previous backup intact and removes only this operation's scratch file.
			data, err := os.ReadFile(output)
			require.NoError(t, err)
			require.Equal(t, "previous backup", string(data))
			entries, err := os.ReadDir(root)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			require.Equal(t, "export.jsonl", entries[0].Name())
		})
	}
}

func TestWriteLibraryExportRenameFailureKeepsDestination(t *testing.T) {
	// Create a conflicting destination after staging starts to exercise an actual rename failure.
	root := t.TempDir()
	output := filepath.Join(root, "export.jsonl")
	err := writeLibraryExport(context.Background(), output, func(writer io.Writer) error {
		require.NoError(t, os.Mkdir(output, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(output, "preserved"), []byte("concurrent destination"), 0600))
		_, err := io.WriteString(writer, "complete backup")
		return err
	})
	require.ErrorContains(t, err, "publish export output failed")

	// Failed publication never removes the destination created by another owner.
	data, err := os.ReadFile(filepath.Join(output, "preserved"))
	require.NoError(t, err)
	require.Equal(t, "concurrent destination", string(data))
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "export.jsonl", entries[0].Name())
}

func TestWriteLibraryExportFollowsExistingAndDanglingSymlinks(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			// Relative final-link chains retain os.Create's target semantics, including a missing target.
			root := t.TempDir()
			target, link := filepath.Join(root, "target.jsonl"), filepath.Join(root, "export.jsonl")
			if existing {
				require.NoError(t, os.WriteFile(target, []byte("previous backup"), 0640))
			}
			require.NoError(t, os.Symlink("target.jsonl", filepath.Join(root, "middle")))
			require.NoError(t, os.Symlink("middle", link))
			require.NoError(t, writeLibraryExport(context.Background(), link, func(writer io.Writer) error {
				_, err := io.WriteString(writer, "complete backup")
				return err
			}))

			// Both links survive publication; the resolved target alone receives the complete new file.
			path, err := os.Readlink(link)
			require.NoError(t, err)
			require.Equal(t, "middle", path)
			path, err = os.Readlink(filepath.Join(root, "middle"))
			require.NoError(t, err)
			require.Equal(t, "target.jsonl", path)
			data, err := os.ReadFile(target)
			require.NoError(t, err)
			require.Equal(t, "complete backup", string(data))
			pending, err := filepath.Glob(filepath.Join(root, ".yatm-export-*"))
			require.NoError(t, err)
			require.Empty(t, pending)
		})
	}

	// A loop is rejected by stdlib resolution without entering the producer or changing either link.
	root := t.TempDir()
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	require.NoError(t, os.Symlink("second", first))
	require.NoError(t, os.Symlink("first", second))
	require.Error(t, writeLibraryExport(context.Background(), first, func(io.Writer) error {
		t.Fatal("symlink loop reached export")
		return nil
	}))
	link, err := os.Readlink(first)
	require.NoError(t, err)
	require.Equal(t, "second", link)
}

func TestWriteLibraryExportResolvesSymlinksBeforeParentTraversal(t *testing.T) {
	for _, throughLink := range []bool{false, true} {
		t.Run(fmt.Sprint(throughLink), func(t *testing.T) {
			// Traversing .. after a directory symlink reaches its physical parent, not the lexical parent.
			root := t.TempDir()
			physical := filepath.Join(root, "physical")
			require.NoError(t, os.MkdirAll(filepath.Join(physical, "nested"), 0755))
			require.NoError(t, os.Symlink(filepath.Join("physical", "nested"), filepath.Join(root, "directory")))
			lexical := filepath.Join(root, "backup.jsonl")
			require.NoError(t, os.WriteFile(lexical, []byte("unrelated backup"), 0600))
			output := root + string(os.PathSeparator) + "directory/../backup.jsonl"
			if throughLink {
				output = filepath.Join(root, "export.jsonl")
				require.NoError(t, os.Symlink("directory/../backup.jsonl", output))
			}

			// Both the staging file and publication belong to the missing target's actual directory.
			var staging string
			require.NoError(t, writeLibraryExport(context.Background(), output, func(writer io.Writer) error {
				staging = writer.(*os.File).Name()
				_, err := io.WriteString(writer, "complete backup")
				return err
			}))
			data, err := os.ReadFile(lexical)
			require.NoError(t, err)
			require.Equal(t, "unrelated backup", string(data))
			data, err = os.ReadFile(filepath.Join(physical, "backup.jsonl"))
			require.NoError(t, err)
			require.Equal(t, "complete backup", string(data))
			resolved, err := filepath.EvalSymlinks(physical)
			require.NoError(t, err)
			require.Equal(t, resolved, filepath.Dir(staging))
			if throughLink {
				link, err := os.Readlink(output)
				require.NoError(t, err)
				require.Equal(t, "directory/../backup.jsonl", link)
			}
		})
	}
}

func TestWriteLibraryExportRejectsMissingSymlinkParents(t *testing.T) {
	if output := os.Getenv("YATM_EXPORT_TEST_MISSING_PARENT"); output != "" {
		// A missing intermediate directory cannot be bypassed by lexically removing its following .. .
		err := writeLibraryExport(context.Background(), output, func(io.Writer) error {
			t.Fatal("invalid parent reached export")
			return nil
		})
		require.ErrorIs(t, err, os.ErrNotExist)
		return
	}

	// Run the known looping case in a child so a resolver regression is bounded and cleaned up.
	root := t.TempDir()
	output := filepath.Join(root, "export.jsonl")
	require.NoError(t, os.Symlink("missing/../export.jsonl", output))
	binary, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "-test.run=^TestWriteLibraryExportRejectsMissingSymlinkParents$")
	command.Env = append(os.Environ(), "YATM_EXPORT_TEST_MISSING_PARENT="+output)
	result, err := command.CombinedOutput()
	require.NoError(t, ctx.Err(), "path resolution failed to reject a missing symlink parent: %s", result)
	require.NoError(t, err, "%s", result)
	link, err := os.Readlink(output)
	require.NoError(t, err)
	require.Equal(t, "missing/../export.jsonl", link)
}

func TestWriteLibraryExportPreservesExplicitDeviceStream(t *testing.T) {
	// Check the provided descriptor before writing so even a regression cannot replace the null device.
	before, err := os.Stat(os.DevNull)
	require.NoError(t, err)
	err = writeLibraryExport(context.Background(), os.DevNull, func(writer io.Writer) error {
		info, err := writer.(*os.File).Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(before, info) {
			return errors.New("special output was replaced with a staging file")
		}
		_, err = io.WriteString(writer, "streamed backup")
		return err
	})
	require.NoError(t, err)

	// Named device output keeps its original filesystem identity after the stream closes.
	after, err := os.Stat(os.DevNull)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after))
}

func exportDescriptorCount() (int, error) {
	// Only names are stable in /dev/fd; statting entries can race the inventory descriptor itself.
	directory, err := os.Open("/dev/fd")
	if err != nil {
		return 0, err
	}
	names, readErr := directory.Readdirnames(-1)
	closeErr := directory.Close()
	return len(names), errors.Join(readErr, closeErr)
}

func TestExportLibraryReleasesResources(t *testing.T) {
	for _, name := range []string{"success", "invalid catalog", "invalid types", "output failure", "canceled"} {
		t.Run(name, func(t *testing.T) {
			// Keep command globals and all created data inside this invocation's fixture.
			root := t.TempDir()
			t.Chdir(root)
			hooks := logrus.StandardLogger().ReplaceHooks(logrus.LevelHooks{})
			t.Cleanup(func() { logrus.StandardLogger().ReplaceHooks(hooks) })
			gc := debug.SetGCPercent(-1)
			t.Cleanup(func() { debug.SetGCPercent(gc) })
			database := filepath.Join(root, "library.db")
			configuration := filepath.Join(root, "config.yaml")
			require.NoError(t, os.WriteFile(configuration, []byte(fmt.Sprintf(
				"database:\n  dialect: sqlite\n  dsn: %q\npaths:\n  work: %q\n", database, root,
			)), 0600))
			if name == "invalid catalog" {
				db, err := resource.OpenSQLite(database)
				require.NoError(t, err)
				sqlDB, err := db.DB()
				require.NoError(t, err)
				defer sqlDB.Close()
				require.NoError(t, db.Exec("CREATE TABLE legacy (id INTEGER)").Error)
				require.NoError(t, sqlDB.Close())
			}

			// Exercise exits both before and after acquiring an output handle.
			types, output := "file", filepath.Join(root, "export.jsonl")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch name {
			case "invalid types":
				types = "unknown"
			case "output failure":
				output = filepath.Join(root, "missing", "export.jsonl")
			case "canceled":
				cancel()
			}
			before, err := exportDescriptorCount()
			if err != nil {
				t.Skipf("descriptor inventory unavailable: %v", err)
			}
			err = exportLibrary(ctx, configuration, types, output)
			if name == "success" {
				require.NoError(t, err)
				data, err := os.ReadFile(output)
				require.NoError(t, err)
				require.Contains(t, string(data), "yatm-library-backup")
			} else {
				require.Error(t, err)
				if name == "canceled" {
					require.ErrorIs(t, err, context.Canceled)
				}
			}

			// SQLite, the rotating log and output close before returning, without finalizer help.
			after, err := exportDescriptorCount()
			require.NoError(t, err)
			require.LessOrEqual(t, after, before)
		})
	}
}
