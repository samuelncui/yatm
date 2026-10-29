package executor

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func installTarFixtureScript(t *testing.T, exe *Executor, fixture *tarDeviceFixture) {
	t.Helper()
	// Only this external adapter can supply the synthetic drive key, and it rejects overlapping raw I/O.
	directory := t.TempDir()
	fixture.scriptKey, fixture.scriptGuard = filepath.Join(directory, "applied"), filepath.Join(directory, "open")
	exe.scripts.Encrypt = writeExecutorTestScript(t, "encrypt", fmt.Sprintf(
		"#!/bin/sh\nset -eu\ntest ! -e %q\ntest -n \"$DEVICE\"\ntest -n \"$TAPE_BARCODE\"\ntest -n \"$TAPE_DIR\"\ncp \"$KEY_FILE\" %q\n", fixture.scriptGuard, fixture.scriptKey))
}

func tarEncryptedReadTarget(stored *library.Media) *entity.ReadMediaTarget {
	target := (&entity.ReadTapeTarget{Device: "/dev/nst0", MediaId: proto.Int64(stored.ID)}).Pack()
	target.ExpectedMediaId, target.ExpectedIdentity, target.ExpectedProfile = stored.ID, stored.Identity, stored.Profile
	return target
}

// formatTarFixture registers an empty cartridge through production FORMAT and opens a fresh attempt.
func formatTarFixture(t *testing.T, exe *Executor, backend *mediaBackend, jobID int64) *library.Media {
	t.Helper()
	ctx := context.Background()
	write, err := backend.NewWriteSession(ctx, tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE))
	require.NoError(t, err)
	result, err := write.Finalize(ctx, nil)
	require.NoError(t, err)
	stored, err := exe.Lib().CreateMedia(ctx, &library.Media{Kind: result.Media.Kind,
		Identity: result.Media.Identity, Profile: result.Media.Profile})
	require.NoError(t, err)
	exe.endAttempt(jobID)
	require.NoError(t, exe.beginAttempt(jobID, func(error) {}))
	return stored
}

func TestTarScriptFormatAppendAndSelectedRead(t *testing.T) {
	// Use real factories and ACP, with an external script and synthetic SSC device only.
	ctx := context.Background()
	exe, backend, fixture, jobID := tarFactoryFixture(t)
	var diagnostics bytes.Buffer
	backend.logger.SetOutput(&diagnostics)
	write, err := newTarFixtureWriteSession(backend.NewWriteSession(ctx, tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE)))
	require.NoError(t, err)
	require.Equal(t, 1, countTarScriptEvents(fixture))
	events := strings.Join(fixture.securityEvents, ",")
	require.Contains(t, events, "rewind")
	require.Less(t, strings.Index(events, "rewind"), strings.Index(events, "script"))
	require.Less(t, strings.Index(events, "script"), strings.Index(events, "write"))

	// Content and label use the same adapter-provided key and normal unload policy.
	data := bytes.Repeat([]byte("script encrypted member\n"), 1000)
	tarWriteFixtureMember(t, write, "first.bin", data)
	result, err := write.Finalize(ctx, nil)
	require.NoError(t, err)
	require.NotEmpty(t, result.Media.Profile.GetTape().Encryption)
	key, err := decodeTarKey(result.Media.Profile.GetTape().Encryption)
	require.NoError(t, err)
	defer clear(key)
	for _, record := range fixture.records {
		if !record.mark {
			require.Equal(t, key, record.key)
		}
	}
	require.Nil(t, fixture.cipherKey, "the configured device policy clears on normal unload")
	var member *mediapkg.CompletedFile
	require.NoError(t, tarFixtureInventory(write, result, ctx, func(value *mediapkg.CompletedFile) error { member = value; return nil }))
	require.NotNil(t, member)

	// Successful publication keeps its business key without any automatic recovery side files.
	copies, err := filepath.Glob(filepath.Join(exe.jobWorkPath(jobID), "tapes", "ABC001", "recovery-*"))
	require.NoError(t, err)
	require.Empty(t, copies)
	stored, err := exe.Lib().CreateMedia(ctx, &library.Media{Kind: result.Media.Kind, Identity: result.Media.Identity, Profile: result.Media.Profile})
	require.NoError(t, err)

	// APPEND runs the script once before the label and tail, then writes without a second LOAD.
	exe.endAttempt(jobID)
	require.NoError(t, exe.beginAttempt(jobID, func(error) {}))
	fixture.securityEvents = nil
	appendSession, err := newTarFixtureWriteSession(backend.NewWriteSession(ctx, (&entity.ArchiveTapeTarget{
		Device: "/dev/nst0", Barcode: "ABC001", Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND,
	}).Pack()))
	require.NoError(t, err)
	require.Equal(t, 1, countTarScriptEvents(fixture))
	require.Equal(t, "script", fixture.securityEvents[0])
	tarWriteFixtureMember(t, appendSession, "later.txt", []byte("later encrypted member"))
	appended, err := appendSession.Finalize(ctx, nil)
	require.NoError(t, err)
	require.True(t, proto.Equal(result.Media.Profile, appended.Media.Profile))

	// Explicit Media selection supplies the saved key and restores the exact member bytes.
	exe.endAttempt(jobID)
	require.NoError(t, exe.beginAttempt(jobID, func(error) {}))
	fixture.securityEvents = nil
	read, err := backend.NewReadSession(ctx, tarEncryptedReadTarget(stored))
	require.NoError(t, err)
	require.Equal(t, 1, countTarScriptEvents(fixture))
	source, err := read.Source(mediapkg.ReadRequest{Path: member.Path, Storage: member.Storage})
	require.NoError(t, err)
	destination := filepath.Join(t.TempDir(), "restored")
	runTarFixtureStream(t, &tarFixtureItem{SimpleJob: &acp.SimpleJob{Path: member.Path, Dsts: []string{destination}}, source: source.Content})
	actual, err := os.ReadFile(destination)
	require.NoError(t, err)
	require.Equal(t, data, actual)
	require.NoError(t, read.Finalize(ctx, nil))
	require.Equal(t, fixture.opens, fixture.closes)
	require.NotContains(t, diagnostics.String(), stored.Profile.GetTape().Encryption)
	require.NotContains(t, diagnostics.String(), hex.EncodeToString(key))
}

func countTarScriptEvents(fixture *tarDeviceFixture) int {
	count := 0
	for _, event := range fixture.securityEvents {
		if event == "script" {
			count++
		}
	}
	return count
}

func TestTarScriptFailureAndWrongKeyDoNotWrite(t *testing.T) {
	for _, operation := range []string{"FORMAT", "READ", "APPEND"} {
		for _, failure := range []string{"script exit", "wrong key", "invalid key"} {
			if operation == "FORMAT" && failure != "script exit" {
				continue
			}
			t.Run(operation+"/"+failure, func(t *testing.T) {
				// Complete a cartridge before injecting a failed preparation; retain all existing records.
				ctx := context.Background()
				exe, backend, fixture, jobID := tarFactoryFixture(t)
				var stored *library.Media
				if operation != "FORMAT" {
					write, err := backend.NewWriteSession(ctx, tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE))
					require.NoError(t, err)
					result, err := write.Finalize(ctx, nil)
					require.NoError(t, err)
					if failure == "wrong key" {
						result.Media.Profile.GetTape().Encryption = "v1:" + strings.Repeat("01", 32)
					}
					if failure == "invalid key" {
						result.Media.Profile.GetTape().Encryption = "v1:zz"
					}
					stored, err = exe.Lib().CreateMedia(ctx, &library.Media{Kind: result.Media.Kind, Identity: result.Media.Identity, Profile: result.Media.Profile})
					require.NoError(t, err)
					exe.endAttempt(jobID)
					require.NoError(t, exe.beginAttempt(jobID, func(error) {}))
				}

				// Inject only the adapter failure after preparing any existing cartridge.
				if failure == "script exit" {
					exe.scripts.Encrypt = writeExecutorTestScript(t, "fail", "#!/bin/sh\nexit 23\n")
				}
				before := fixture.writes

				// A failed script or label check has no plaintext fallback, rekey retry or data WRITE.
				var err error
				switch operation {
				case "FORMAT":
					_, err = backend.NewWriteSession(ctx, tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE))
				case "READ":
					_, err = backend.NewReadSession(ctx, tarEncryptedReadTarget(stored))
				case "APPEND":
					_, err = backend.NewWriteSession(ctx, (&entity.ArchiveTapeTarget{Device: "/dev/nst0", Barcode: "ABC001", Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND}).Pack())
				}
				require.Error(t, err)
				if failure == "script exit" {
					require.ErrorContains(t, err, "exit status 23")
				}
				require.Equal(t, before, fixture.writes)
				require.Equal(t, fixture.opens, fixture.closes)
			})
		}
	}
}

func TestTarScriptUnknownAlgorithmRemainsReadable(t *testing.T) {
	// A successful configured script need not expose a recognized algorithm through the drive report.
	ctx := context.Background()
	exe, backend, fixture, jobID := tarFactoryFixture(t)
	fixture.hideAlgorithm = true
	write, err := backend.NewWriteSession(ctx, tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE))
	require.NoError(t, err)
	result, err := write.Finalize(ctx, nil)
	require.NoError(t, err)
	require.NotEmpty(t, result.Media.Profile.GetTape().Encryption)
	require.Nil(t, result.Media.Profile.GetTape().Tar.EncryptionAlgorithm)
	stored, err := exe.Lib().CreateMedia(ctx, &library.Media{Kind: result.Media.Kind, Identity: result.Media.Identity, Profile: result.Media.Profile})
	require.NoError(t, err)

	// Unknown algorithm conditions cannot become a plaintext model; ordinary read and APPEND still work.
	exe.endAttempt(jobID)
	require.NoError(t, exe.beginAttempt(jobID, func(error) {}))
	read, err := backend.NewReadSession(ctx, tarEncryptedReadTarget(stored))
	require.NoError(t, err)
	require.NoError(t, read.Finalize(ctx, nil))
	exe.endAttempt(jobID)
	require.NoError(t, exe.beginAttempt(jobID, func(error) {}))
	appendSession, err := backend.NewWriteSession(ctx, (&entity.ArchiveTapeTarget{Device: "/dev/nst0", Barcode: "ABC001", Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND}).Pack())
	require.NoError(t, err)
	_, err = appendSession.Finalize(ctx, nil)
	require.NoError(t, err)
}

func TestTarMismatchedBarcodeNeverRunsEncryptScript(t *testing.T) {
	for _, operation := range []string{"FORMAT", "READ", "APPEND"} {
		t.Run(operation, func(t *testing.T) {
			// Encrypted READ/APPEND may use only the selected cartridge's key; FORMAT also checks before its adapter.
			ctx := context.Background()
			exe, backend, fixture, jobID := tarFactoryFixture(t)
			var stored *library.Media
			if operation != "FORMAT" {
				stored = formatTarFixture(t, exe, backend, jobID)
			}
			fixture.barcode, fixture.securityEvents = "OTHER1L5", nil
			exe.scripts.Encrypt = writeExecutorTestScript(t, "must-not-encrypt", "#!/bin/sh\nexit 43\n")
			before := fixture.writes

			// A nonempty conflicting MAM identity stops before script execution, repositioning or writing.
			var err error
			switch operation {
			case "FORMAT":
				_, err = backend.NewWriteSession(ctx, tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE))
			case "READ":
				_, err = backend.NewReadSession(ctx, tarEncryptedReadTarget(stored))
			case "APPEND":
				_, err = backend.NewWriteSession(ctx, (&entity.ArchiveTapeTarget{
					Device: "/dev/nst0", Barcode: "ABC001", Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND,
				}).Pack())
			}
			require.ErrorContains(t, err, "tar Tape barcode differs")
			require.NotContains(t, err.Error(), "exit status 43")
			require.Equal(t, before, fixture.writes)
			require.Zero(t, fixture.barcodeWrites)
			require.Equal(t, "OTHER1L5", fixture.barcode)
			require.Zero(t, countTarScriptEvents(fixture))
			require.NotContains(t, fixture.securityEvents, "mode")
			require.NotContains(t, fixture.securityEvents, "rewind")
			require.Equal(t, fixture.opens, fixture.closes)
		})
	}
}

func TestObservedTapeEncryptionAlgorithmRequiresCurrentUnambiguousReport(t *testing.T) {
	// Only the current enabled write algorithm can become an immutable public label fact.
	algorithm := "AES-256-GCM-128"
	for _, test := range []struct {
		name  string
		setup func(*entity.InspectDeviceResponse)
		known bool
	}{
		{name: "valid", known: true},
		{name: "missing report"},
		{name: "missing Tape", setup: func(report *entity.InspectDeviceResponse) { report.Report = nil }},
		{name: "missing feature", setup: func(report *entity.InspectDeviceResponse) { report.GetTape().Features = nil }},
		{name: "missing limits", setup: func(report *entity.InspectDeviceResponse) { report.GetTape().Features[0].Limits = nil }},
		{name: "missing current index", setup: func(report *entity.InspectDeviceResponse) {
			report.GetTape().Features[0].GetEncryption().CurrentAlgorithmIndex = nil
		}},
		{name: "missing write mode", setup: func(report *entity.InspectDeviceResponse) {
			report.GetTape().Features[0].GetEncryption().EncryptionMode = nil
		}},
		{name: "write off", setup: func(report *entity.InspectDeviceResponse) {
			report.GetTape().Features[0].GetEncryption().EncryptionMode = proto.Uint32(0)
		}},
		{name: "unknown current index", setup: func(report *entity.InspectDeviceResponse) {
			report.GetTape().Features[0].GetEncryption().CurrentAlgorithmIndex = proto.Uint32(8)
		}},
		{name: "unrecognized current algorithm", setup: func(report *entity.InspectDeviceResponse) {
			report.GetTape().Features[0].GetEncryption().Algorithms[0].Name = ""
		}},
		{name: "ambiguous index", setup: func(report *entity.InspectDeviceResponse) {
			limits := report.GetTape().Features[0].GetEncryption()
			limits.Algorithms = append(limits.Algorithms, &entity.TapeEncryptionAlgorithm{Index: 7, Name: "another algorithm"})
		}},
		{name: "agreeing repeated observation", setup: func(report *entity.InspectDeviceResponse) {
			report.GetTape().Features = append(report.GetTape().Features, proto.Clone(report.GetTape().Features[0]).(*entity.DeviceFeature))
		}, known: true},
		{name: "conflicting observations", setup: func(report *entity.InspectDeviceResponse) {
			other := proto.Clone(report.GetTape().Features[0]).(*entity.DeviceFeature)
			other.GetEncryption().Algorithms[0].Name = "another algorithm"
			report.GetTape().Features = append(report.GetTape().Features, other)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Model optional diagnostic fields directly; a nil observation must remain absent.
			var report *entity.InspectDeviceResponse
			if test.name != "missing report" {
				report = &entity.InspectDeviceResponse{Report: &entity.InspectDeviceResponse_Tape{Tape: &entity.TapeDeviceReport{
					Features: []*entity.DeviceFeature{{Feature: entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_ENCRYPTION,
						Limits: &entity.DeviceFeature_Encryption{Encryption: &entity.TapeEncryptionLimits{
							CurrentAlgorithmIndex: proto.Uint32(7), EncryptionMode: proto.Uint32(2),
							Algorithms: []*entity.TapeEncryptionAlgorithm{{Index: 7, Name: algorithm, KeyBytes: 32}},
						}},
					}},
				}}}
				if test.setup != nil {
					test.setup(report)
				}
			}
			before := proto.Clone(report)

			// Reading an algorithm neither mutates the diagnostic snapshot nor invents a default.
			actual := observedTapeEncryptionAlgorithm(report)
			if test.known {
				require.NotNil(t, actual)
				require.Equal(t, algorithm, *actual)
			} else {
				require.Nil(t, actual)
			}
			require.True(t, proto.Equal(before, report))
		})
	}
}
