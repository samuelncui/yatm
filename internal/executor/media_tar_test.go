package executor

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type tarFixtureItem struct {
	*acp.SimpleJob
	source  acp.ContentSource
	targets []acp.ContentTarget
}

func (i *tarFixtureItem) ContentSource() acp.ContentSource    { return i.source }
func (i *tarFixtureItem) ContentTargets() []acp.ContentTarget { return i.targets }

func runTarFixtureStream(t *testing.T, item *tarFixtureItem) acp.Result {
	t.Helper()
	// Exercise the real transfer engine against the prepared Session endpoint and ordinary files.
	var results []acp.Result
	engine, err := acp.NewStream(context.Background(), func(batch []acp.Result) error {
		results = append(results, batch...)
		return nil
	}, acp.WithHashPolicy(acp.HashReadRefresh),
		acp.SetFromDevice(acp.LinearDevice(true)), acp.SetToDevice(acp.LinearDevice(true)))
	require.NoError(t, err)
	require.NoError(t, engine.Submit(item))
	require.NoError(t, engine.Close())
	require.NoError(t, engine.Wait())
	require.Len(t, results, 1)
	require.NoError(t, results[0].Err)
	require.Len(t, results[0].Targets, 1)
	require.NoError(t, results[0].Targets[0].Err)
	return results[0]
}

func tarFactoryFixture(t *testing.T) (*Executor, *mediaBackend, *tarDeviceFixture, int64) {
	t.Helper()
	// Use the real Catalog/Settings and attempt lifecycle; only SSC hardware responses are synthetic.
	exe := setupTestExecutor(t)
	require.NoError(t, exe.Lib().AutoMigrate())
	fixture := newTarDeviceFixture()
	installTarFixtureScript(t, exe, fixture)
	exe.tapeProvider = fixture
	job := createTestCatalogJob(t, exe, entity.JobKind_JOB_KIND_ARCHIVE)
	require.NoError(t, exe.beginAttempt(job.ID, func(error) {}))
	t.Cleanup(func() { exe.endAttempt(job.ID) })
	return exe, exe.NewMediaBackend(job.ID, nil, nil).(*mediaBackend), fixture, job.ID
}

// tarFixtureWriteSession retains receipts for bounded fixtures, where Archive's item table is not present.
type tarFixtureWriteSession struct {
	mediapkg.WriteSession
	targets []*tarFixtureTarget
}

type tarFixtureTarget struct {
	ref  mediapkg.TargetRef
	path string
	size int64
}

func newTarFixtureWriteSession(session mediapkg.WriteSession, err error) (mediapkg.WriteSession, error) {
	if err != nil {
		return nil, err
	}
	return &tarFixtureWriteSession{WriteSession: session}, nil
}

func (s *tarFixtureWriteSession) Target(path string, size int64) (mediapkg.TargetRef, error) {
	ref, err := s.WriteSession.Target(path, size)
	if err != nil {
		return ref, err
	}
	target := &tarFixtureTarget{ref: ref, path: path}
	s.targets = append(s.targets, target)
	return mediapkg.TargetRef{Content: target}, nil
}

func (t *tarFixtureTarget) Open(ctx context.Context, metadata acp.ContentMetadata) (acp.ContentWriter, error) {
	if metadata.Info != nil {
		t.size = metadata.Info.Size()
	}
	return t.ref.Content.Open(ctx, metadata)
}

func tarFixtureInventory(session mediapkg.WriteSession, result *mediapkg.WriteResult, ctx context.Context,
	emit func(*mediapkg.CompletedFile) error,
) error {
	// The test replays its own target receipts through the real final validator, never a compatibility inventory.
	if result.Inventory != nil {
		return errors.New("tar fixture returned an independent inventory")
	}
	for _, target := range session.(*tarFixtureWriteSession).targets {
		if err := ctx.Err(); err != nil {
			return err
		}
		staged := target.ref.Storage()
		if staged == nil {
			continue
		}
		storage, err := result.FinalizeStorage(staged)
		if err != nil {
			return err
		}
		if storage != nil {
			if err := emit(&mediapkg.CompletedFile{Path: result.PathPrefix + "/" + target.path, Size: target.size, Storage: storage}); err != nil {
				return err
			}
		}
	}
	return nil
}

func tarFormatTarget(mode entity.TapeRecordMode) *entity.ArchiveMediaTarget {
	return (&entity.ArchiveTapeTarget{
		Device: "/dev/nst0", Barcode: "ABC001", Name: "Tar fixture", Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT,
		Format: proto.String(library.TapeFormatTarPAXV1), TarOptions: &entity.TarWriteOptions{RecordMode: mode.Enum(), RecordBytes: proto.Uint32(4096)},
	}).Pack()
}

func tarWriteFixtureMember(t *testing.T, session mediapkg.WriteSession, name string, data []byte) acp.Result {
	t.Helper()
	// ACP observes the native source once and writes its actual metadata through the tar endpoint.
	filename := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.WriteFile(filename, data, 0600))
	target, err := session.Target(name, int64(len(data)))
	require.NoError(t, err)
	return runTarFixtureStream(t, &tarFixtureItem{
		SimpleJob: &acp.SimpleJob{Path: filename, Dsts: []string{name}}, targets: []acp.ContentTarget{target.Content},
	})
}

func TestTarFactoryFormatAppendInventoryAndSelectedACPRead(t *testing.T) {
	for _, tc := range []struct {
		mode   entity.TapeRecordMode
		opcode byte
	}{
		{entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED, 0x92},
		{entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, 0x92},
		{entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED, 0x2b},
		{entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, 0x2b},
	} {
		t.Run(fmt.Sprintf("%s/LOCATE_%02x", tc.mode, tc.opcode), func(t *testing.T) {
			// FORMAT produces a complete identity, an ordinary content archive and restorable record policy.
			ctx := context.Background()
			exe, backend, fixture, jobID := tarFactoryFixture(t)
			fixture.locate10, fixture.shortPosition = tc.opcode == 0x2b, tc.opcode == 0x2b
			fixture.positionUnknownUntilMotion = true
			originalMode := append([]byte(nil), fixture.mode...)
			write, err := newTarFixtureWriteSession(backend.NewWriteSession(ctx, tarFormatTarget(tc.mode)))
			require.NoError(t, err)
			data := make([]byte, 12<<10)
			for i := range data {
				data[i] = byte(i)
			}
			copied := tarWriteFixtureMember(t, write, "data.bin", data)
			result, err := write.Finalize(ctx, nil)
			require.NoError(t, err)
			require.Equal(t, originalMode, fixture.mode)
			require.Equal(t, fixture.opens, fixture.closes)
			var member *mediapkg.CompletedFile
			require.NoError(t, tarFixtureInventory(write, result, ctx, func(value *mediapkg.CompletedFile) error { member = value; return nil }))
			require.NotNil(t, member)
			require.Equal(t, uint64(1), member.Storage.Metadata.GetTar().TapeFile)
			require.NotNil(t, member.Storage.Metadata.GetTar().Header, "standard SSC objects and fresh boundary observations establish optional anchors")
			require.NotNil(t, member.Storage.Metadata.GetTar().End)
			require.Equal(t, int64(len(data)), member.Size)
			parameters, err := filepath.Glob(filepath.Join(exe.jobWorkPath(jobID), "tapes", "ABC001", "recovery-*.json"))
			require.NoError(t, err)
			require.Empty(t, parameters)
			stored, err := exe.Lib().CreateMedia(ctx, &library.Media{
				Kind: result.Media.Kind, Identity: result.Media.Identity, Name: result.Media.Name,
				Profile: result.Media.Profile,
			})
			require.NoError(t, err)

			// APPEND derives its policy from the cartridge, even after the device default changes.
			_, err = exe.settings.Device.Save(ctx, &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{
				Device: "/dev/nst0", Options: &entity.DeviceConfig_Tape{Tape: &entity.TapeDeviceSettings{
					DefaultFormat: proto.String(library.TapeFormatLTFSV1), TarWriteOptions: &entity.TarWriteOptions{
						RecordBytes: proto.Uint32(8192), HardwareCompression: proto.Bool(true),
					},
				}},
			}}})
			require.NoError(t, err)
			exe.endAttempt(jobID)
			require.NoError(t, exe.beginAttempt(jobID, func(error) {}))
			appendTarget := (&entity.ArchiveTapeTarget{
				Device: "/dev/nst0", Barcode: "ABC001", Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND,
			}).Pack()

			// The cartridge memory barcode is the Tape identity; APPEND never substitutes the label for it.
			barcode, writes := fixture.barcode, fixture.writes
			fixture.barcode = ""
			_, err = backend.NewWriteSession(ctx, appendTarget)
			require.ErrorContains(t, err, "tar Tape identity is unavailable")
			require.Equal(t, writes, fixture.writes)
			require.Equal(t, fixture.opens, fixture.closes)
			fixture.barcode = barcode
			exe.endAttempt(jobID)
			require.NoError(t, exe.beginAttempt(jobID, func(error) {}))

			// The bounded tail proof needs long READ POSITION; short-only drives refuse APPEND without writing.
			if fixture.shortPosition {
				_, err = backend.NewWriteSession(ctx, appendTarget)
				require.ErrorContains(t, err, "APPEND requires long READ POSITION and LOCATE support")
				require.Equal(t, writes, fixture.writes)
				require.Equal(t, fixture.opens, fixture.closes)
				exe.endAttempt(jobID)
				require.NoError(t, exe.beginAttempt(jobID, func(error) {}))
				fixture.shortPosition = false
			}
			appendSession, err := newTarFixtureWriteSession(backend.NewWriteSession(ctx, appendTarget))
			require.NoError(t, err)
			tarWriteFixtureMember(t, appendSession, "later.txt", []byte("later archive"))
			appended, err := appendSession.Finalize(ctx, nil)
			require.NoError(t, err)
			fixture.shortPosition = tc.opcode == 0x2b
			require.NoError(t, tarFixtureInventory(appendSession, appended, ctx, func(value *mediapkg.CompletedFile) error {
				require.Equal(t, uint64(2), value.Storage.Metadata.GetTar().TapeFile)
				require.NotNil(t, value.Storage.Metadata.GetTar().Header)
				require.NotNil(t, value.Storage.Metadata.GetTar().End)
				return nil
			}))

			// The prepared read factory validates the label and exposes the same frozen member to ACP.
			exe.endAttempt(jobID)
			require.NoError(t, exe.beginAttempt(jobID, func(error) {}))
			target := (&entity.ReadTapeTarget{Device: "/dev/nst0", MediaId: proto.Int64(stored.ID)}).Pack()
			target.ExpectedMediaId, target.ExpectedIdentity, target.ExpectedProfile = stored.ID, stored.Identity, stored.Profile
			read, err := backend.NewReadSession(ctx, target)
			require.NoError(t, err)
			var observed []string
			require.NoError(t, read.(mediapkg.InventorySession).WalkInventory(ctx, func(value *mediapkg.InventoryEntry) error {
				require.NotNil(t, value.Storage.Metadata.GetTar().Header)
				require.NotNil(t, value.Storage.Metadata.GetTar().End)
				if value.Path == member.Path {
					require.True(t, proto.Equal(member.Storage.Metadata.GetTar(), value.Storage.Metadata.GetTar()))
				}
				observed = append(observed, value.Path)
				return nil
			}))
			require.Equal(t, []string{member.Path, "archives/0000000000000002/later.txt"}, observed)
			beforeLocate := len(fixture.locateCommands)
			source, err := read.Source(mediapkg.ReadRequest{Path: member.Path, Storage: member.Storage})
			require.NoError(t, err)
			output := filepath.Join(t.TempDir(), "restored")
			restored := runTarFixtureStream(t, &tarFixtureItem{
				SimpleJob: &acp.SimpleJob{Path: member.Path, Dsts: []string{output}}, source: source.Content,
			})
			require.Len(t, fixture.locateCommands, beforeLocate+1)
			require.Equal(t, tc.opcode, fixture.locateCommands[beforeLocate][0])
			require.Equal(t, copied.SHA256, restored.SHA256)
			actual, err := os.ReadFile(output)
			require.NoError(t, err)
			require.Equal(t, data, actual)
			hash := sha256.Sum256(data)
			require.Equal(t, hash[:], restored.SHA256)
			require.NoError(t, read.Finalize(ctx, nil))
			require.Equal(t, originalMode, fixture.mode)
			require.Equal(t, fixture.opens, fixture.closes)
			require.GreaterOrEqual(t, fixture.opens, 3, "FORMAT, APPEND and READ use separate descriptors")
			action := byte(6)
			if tc.opcode == 0x2b {
				action = 0
			}
			require.Contains(t, fixture.positionCommands, action)
		})
	}
}

func TestTarFactoryCachedCapacityClosesBeforeMemberWithoutStreamingQueries(t *testing.T) {
	// A synthetic zero-space estimate is refreshed after the identity file is physically complete.
	ctx := context.Background()
	exe, backend, fixture, _ := tarFactoryFixture(t)
	fixture.capacityEOD, fixture.capacityEW = proto.Uint32(1), proto.Uint32(1)
	_, err := exe.settings.Device.Save(ctx, &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{
		Device: "/dev/nst0", Options: &entity.DeviceConfig_Tape{Tape: &entity.TapeDeviceSettings{CapacityPollIntervalSeconds: proto.Uint32(0)}},
	}}})
	require.NoError(t, err)
	write, err := newTarFixtureWriteSession(backend.NewWriteSession(ctx, tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE)))
	require.NoError(t, err)
	require.Equal(t, []int{0, 0, 1}, fixture.capacityQueries)
	before := fixture.writes
	source := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.WriteFile(source, []byte("pending content"), 0600))
	info, err := os.Stat(source)
	require.NoError(t, err)

	// Manifest reference construction cannot consume space; actual metadata is checked at ordered Open.
	target, err := write.Target("data.bin", 0)
	require.NoError(t, err)
	_, err = target.Content.Open(ctx, acp.ContentMetadata{Info: info})
	require.ErrorIs(t, err, mediapkg.ErrTargetNoSpace)
	require.Equal(t, before, fixture.writes)
	result, err := write.Finalize(ctx, err)
	require.NoError(t, err)
	require.ErrorIs(t, result.Stop, mediapkg.ErrTargetNoSpace)
	require.Equal(t, []int{0, 0, 1}, fixture.capacityQueries, "explicit Off must not issue during content/finalization")
	require.NoError(t, tarFixtureInventory(write, result, ctx, func(*mediapkg.CompletedFile) error {
		t.Fatal("a refused member cannot publish")
		return nil
	}))

	// The refused member leaves a normally terminated empty tar, not a fabricated truncated member.
	var content bytes.Buffer
	file := 0
	for _, record := range fixture.records {
		if record.mark {
			file++
			continue
		}
		if file == 1 {
			_, err := content.Write(record.data)
			require.NoError(t, err)
		}
	}
	require.Equal(t, 1024, content.Len())
	_, err = tar.NewReader(&content).Next()
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, 2, fixture.filemarks)
	require.Equal(t, fixture.opens, fixture.closes)
}

func TestTarFactoryRefusesUnterminatedAppendWithoutWrite(t *testing.T) {
	// A valid earlier archive does not authorize overwriting unsealable trailing records.
	ctx := context.Background()
	exe, backend, fixture, _ := tarFactoryFixture(t)
	write, err := newTarFixtureWriteSession(backend.NewWriteSession(ctx, tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE)))
	require.NoError(t, err)
	result, err := write.Finalize(ctx, nil)
	require.NoError(t, err)
	_, err = exe.Lib().CreateMedia(ctx, &library.Media{Kind: result.Media.Kind, Identity: result.Media.Identity, Profile: result.Media.Profile})
	require.NoError(t, err)
	fixture.records = append(fixture.records, tarFixtureRecord{data: []byte("unterminated tail")})
	before := fixture.writes
	_, err = newTarFixtureWriteSession(backend.NewWriteSession(ctx, (&entity.ArchiveTapeTarget{
		Device: "/dev/nst0", Barcode: "ABC001", Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND,
	}).Pack()))
	require.ErrorContains(t, err, "terminating filemark")
	require.Equal(t, before, fixture.writes)
	require.Equal(t, fixture.opens, fixture.closes)
}

func TestTarFactoryUnknownCompletionWithholdsPublication(t *testing.T) {
	for _, failMark := range []int{1, 2} {
		t.Run(string(rune('0'+failMark)), func(t *testing.T) {
			// Unknown label or content completion withholds publication and leaves the drive unavailable.
			ctx := context.Background()
			exe, backend, fixture, jobID := tarFactoryFixture(t)
			fixture.failMark = failMark
			session, err := newTarFixtureWriteSession(backend.NewWriteSession(ctx, tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE)))
			if failMark == 2 {
				require.NoError(t, err)
				result, finalizeErr := session.Finalize(ctx, nil)
				require.Nil(t, result)
				err = finalizeErr
			}
			require.Error(t, err)
			exe.endAttempt(jobID)
			require.Empty(t, exe.ListAvailableDevices())
			parameters, globErr := filepath.Glob(filepath.Join(exe.jobWorkPath(jobID), "tapes", "ABC001", "recovery-*.json"))
			require.NoError(t, globErr)
			require.Empty(t, parameters)
			stored, lookupErr := exe.Lib().GetMediaByIdentity(ctx, entity.MediaKind_MEDIA_KIND_TAPE, "ABC001")
			require.NoError(t, lookupErr)
			require.Nil(t, stored)
		})
	}

	// A cleanup failure also invalidates an otherwise completed stream and retains the drive.
	ctx := context.Background()
	exe, backend, fixture, jobID := tarFactoryFixture(t)
	session, err := newTarFixtureWriteSession(backend.NewWriteSession(ctx, tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE)))
	require.NoError(t, err)
	fixture.cleanupError = errors.New("cleanup failed")
	result, err := session.Finalize(ctx, nil)
	require.Nil(t, result)
	require.ErrorIs(t, err, mediapkg.ErrFinalizeUnusable)
	exe.endAttempt(jobID)
	require.Empty(t, exe.ListAvailableDevices())
}

func TestTarDefaultsStayFrozenBeforeAsynchronousPreparation(t *testing.T) {
	// Admit a real Archive attempt, then change saved defaults before its factory resolves choices.
	ctx := context.Background()
	exe := setupTestExecutor(t)
	job := createTestJob(t, exe, entity.JobKind_JOB_KIND_ARCHIVE, 0)
	waitJobStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_READY)
	prefs := &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{Device: "/dev/nst0", Options: &entity.DeviceConfig_Tape{
		Tape: &entity.TapeDeviceSettings{CapacityPollIntervalSeconds: proto.Uint32(9), DefaultFormat: proto.String(library.TapeFormatTarPAXV1), TarWriteOptions: &entity.TarWriteOptions{
			RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE.Enum(), RecordBytes: proto.Uint32(4096),
		}},
	}}}}
	_, err := exe.settings.Device.Save(ctx, prefs)
	require.NoError(t, err)
	started, release := make(chan struct{}), make(chan struct{}, 1)
	t.Cleanup(func() {
		select {
		case release <- struct{}{}:
		default:
		}
	})
	type resolvedTarget struct {
		value    *entity.ArchiveTapeTarget
		interval time.Duration
		err      error
	}
	resolved := make(chan resolvedTarget, 1)
	request := &entity.ArchiveTapeTarget{Device: "/dev/nst0", Barcode: "ABC001", Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT}
	require.NoError(t, exe.StartJob(ctx, job.ID, entity.JobKind_JOB_KIND_ARCHIVE, func(runCtx context.Context, _ Runner) error {
		close(started)
		<-release
		value, err := exe.NewMediaBackend(job.ID, nil, nil).(*mediaBackend).resolveTapeWrite(runCtx, request)
		defaults, defaultsErr := exe.NewMediaBackend(job.ID, nil, nil).(*mediaBackend).tapeDefaults(runCtx, request.Device)
		resolved <- resolvedTarget{value, settingspkg.TapeCapacityPollInterval(defaults), errors.Join(err, defaultsErr)}
		return err
	}))
	<-started

	// Omitted request values use admission facts, while a later fresh operation observes the new default.
	prefs.Devices[0].GetTape().DefaultFormat = proto.String(library.TapeFormatLTFSV1)
	prefs.Devices[0].GetTape().TarWriteOptions.RecordBytes = proto.Uint32(8192)
	prefs.Devices[0].GetTape().CapacityPollIntervalSeconds = proto.Uint32(0)
	_, err = exe.settings.Device.Save(ctx, prefs)
	require.NoError(t, err)
	release <- struct{}{}
	frozen := <-resolved
	require.NoError(t, frozen.err)
	require.Equal(t, library.TapeFormatTarPAXV1, frozen.value.GetFormat())
	require.EqualValues(t, 4096, frozen.value.TarOptions.GetRecordBytes())
	require.Equal(t, 9*time.Second, frozen.interval)
	require.Nil(t, request.Format)
	require.Nil(t, request.TarOptions)
	waitJobReleased(t, exe, job.ID)
	next, err := exe.NewMediaBackend(job.ID, nil, nil).(*mediaBackend).resolveTapeWrite(ctx, request)
	require.NoError(t, err)
	require.Equal(t, library.TapeFormatLTFSV1, next.GetFormat())
}

func TestTarOptionResolutionRecordSizeDefaultAndOverrides(t *testing.T) {
	for _, tc := range []struct {
		name            string
		saved, explicit *uint32
		expected        uint32
	}{
		{name: "default", expected: 524288},
		{name: "device", saved: proto.Uint32(262144), expected: 262144},
		{name: "FORMAT override", saved: proto.Uint32(262144), explicit: proto.Uint32(1048576), expected: 1048576},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Resolve omission and overrides through persisted Settings without touching hardware.
			ctx := context.Background()
			exe, backend, fixture, _ := tarFactoryFixture(t)
			_, err := exe.settings.Device.Save(ctx, &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{
				Device: "/dev/nst0", Options: &entity.DeviceConfig_Tape{Tape: &entity.TapeDeviceSettings{
					TarWriteOptions: &entity.TarWriteOptions{RecordBytes: tc.saved},
				}},
			}}})
			require.NoError(t, err)
			target := tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE).GetTape()
			target.TarOptions.RecordBytes = tc.explicit
			resolved, err := backend.resolveTapeWrite(ctx, target)
			require.NoError(t, err)
			require.Equal(t, tc.expected, resolved.TarOptions.GetRecordBytes())
			require.Equal(t, tc.explicit, target.TarOptions.RecordBytes)
			require.Zero(t, fixture.opens)

			// Later device recommendations cannot replace the admitted size policy.
			options := mergeTarOptions(&entity.TarWriteOptions{RecordBytes: proto.Uint32(8192)}, resolved.TarOptions)
			require.Equal(t, tc.expected, options.GetRecordBytes())
		})
	}
}

func TestTarOptionResolutionDefersCompressionDefault(t *testing.T) {
	// Admission retains an omitted field so a later qualified device recommendation can fill it.
	_, backend, _, _ := tarFactoryFixture(t)
	target := tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE).GetTape()
	resolved, err := backend.resolveTapeWrite(context.Background(), target)
	require.NoError(t, err)
	require.Nil(t, resolved.TarOptions.HardwareCompression)
	recommended := &entity.TarWriteOptions{HardwareCompression: proto.Bool(true)}
	options := mergeTarOptions(recommended, resolved.TarOptions)
	require.True(t, options.GetHardwareCompression())
	require.Nil(t, target.TarOptions.HardwareCompression)

	// Explicit off still wins over a recommendation, including after the admission merge.
	target.TarOptions.HardwareCompression = proto.Bool(false)
	resolved, err = backend.resolveTapeWrite(context.Background(), target)
	require.NoError(t, err)
	options = mergeTarOptions(recommended, resolved.TarOptions)
	require.NotNil(t, options.HardwareCompression)
	require.False(t, options.GetHardwareCompression())
	require.True(t, recommended.GetHardwareCompression())
}

func TestTarFormatAssignsAbsentMAMBarcode(t *testing.T) {
	mismatch := "OTHER1"
	for _, tc := range []struct {
		name, barcode string
		stored        *string
		writes        int
		err           string
	}{
		{name: "absent", writes: 1},
		{name: "readback mismatch", stored: &mismatch, writes: 1, err: "tar Tape barcode readback differs"},
		{name: "present match", barcode: "ABC001L5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// FORMAT writes an absent cartridge memory barcode and proves it by readback before the identity archive.
			ctx := context.Background()
			_, backend, fixture, _ := tarFactoryFixture(t)
			fixture.barcode, fixture.barcodeStored = tc.barcode, tc.stored
			session, err := backend.NewWriteSession(ctx, tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE))
			require.Equal(t, tc.writes, fixture.barcodeWrites)
			require.Equal(t, fixture.opens, fixture.closes+map[bool]int{false: 0, true: 1}[err == nil])
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
				require.Zero(t, fixture.writes)
				require.Zero(t, countTarScriptEvents(fixture))
				return
			}
			require.NoError(t, err)
			require.Equal(t, map[bool]string{false: "ABC001L5", true: "ABC001"}[tc.writes == 1], fixture.barcode)
			events := strings.Join(fixture.securityEvents, ",")
			if tc.writes == 1 {
				require.Less(t, strings.Index(events, "attribute"), strings.Index(events, "script"))
			}
			require.Less(t, strings.Index(events, "script"), strings.Index(events, "write"))
			_, err = session.Finalize(ctx, nil)
			require.NoError(t, err)
		})
	}
}

func TestTarAutomaticIdentificationUsesOnlyMAMBarcode(t *testing.T) {
	for _, mam := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing MAM", true: "MAM candidate"}[mam], func(t *testing.T) {
			// A fresh read attempt identifies the cartridge by MAM alone, without moving the medium.
			ctx := context.Background()
			exe, backend, fixture, jobID := tarFactoryFixture(t)
			stored := formatTarFixture(t, exe, backend, jobID)
			if !mam {
				fixture.barcode = ""
			}
			before, writes := fixture.cursor, fixture.writes
			exe.endAttempt(jobID)
			require.NoError(t, exe.beginAttempt(jobID, func(error) {}))
			called := 0
			selected, err := exe.ResolveTapeReadMedia(ctx, jobID, &entity.ReadTapeTarget{Device: "/dev/nst0"}, func(_ context.Context, barcode string) (*entity.ReadMediaTarget, error) {
				called++
				require.Equal(t, "ABC001", barcode)
				value := (&entity.ReadTapeTarget{Device: "/dev/nst0", MediaId: proto.Int64(99)}).Pack()
				value.ExpectedMediaId, value.ExpectedIdentity, value.ExpectedProfile = 99, barcode, stored.Profile
				return value, nil
			})
			if mam {
				require.NoError(t, err)
				require.EqualValues(t, 99, selected.ExpectedMediaId)
				require.Equal(t, 1, called)
			} else {
				require.ErrorContains(t, err, "Tape identity is unavailable")
				require.Zero(t, called)
			}
			require.Equal(t, before, fixture.cursor)
			require.Equal(t, writes, fixture.writes)
			require.Equal(t, fixture.opens, fixture.closes)
		})
	}
}

func TestTarFormatLabelWarningClosesAdmissionAtTheCompletedBoundary(t *testing.T) {
	// EW can first arrive at identity completion; it must not start another tar or filemark.
	ctx := context.Background()
	_, backend, fixture, _ := tarFactoryFixture(t)
	fixture.warningMark = 1
	session, err := newTarFixtureWriteSession(backend.NewWriteSession(ctx, tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE)))
	require.NoError(t, err)
	writes := fixture.writes
	_, err = session.Target("never-started", 1)
	require.ErrorIs(t, err, mediapkg.ErrTargetNoSpace)
	result, err := session.Finalize(ctx, mediapkg.ErrTargetNoSpace)
	require.NoError(t, err)
	require.NotNil(t, result.Media)
	count := 0
	require.NoError(t, tarFixtureInventory(session, result, ctx, func(*mediapkg.CompletedFile) error { count++; return nil }))
	require.Zero(t, count)
	require.Equal(t, writes, fixture.writes)
	require.Equal(t, 1, fixture.filemarks)
	require.Equal(t, fixture.opens, fixture.closes)
}

func TestTarPolicyLogContainsPublicOptionsWithoutEncodedKey(t *testing.T) {
	// Even an encrypted profile supplies only whitelisted public facts to the operator log.
	_, backend, _, _ := tarFactoryFixture(t)
	var output strings.Builder
	backend.logger.SetOutput(&output)
	backend.logTarPolicy(context.Background(), "/dev/nst0", "FORMAT", &entity.TapeMediaProfile{
		Format: library.TapeFormatTarPAXV1, Encryption: "v1:retained-secret-key", Tar: &entity.TarMediaProfile{
			InitializationId: "public-initialization", RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 4096,
		},
	})
	require.Contains(t, output.String(), "record_bytes=4096")
	require.NotContains(t, output.String(), "retained-secret-key")
	require.NotContains(t, output.String(), "v1:")
}
