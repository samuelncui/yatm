package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"
)

func ltfsSGLabelFacts() *mediapkg.LTFSLabelFacts {
	return &mediapkg.LTFSLabelFacts{VolumeUUID: "204d1c2f-c755-44cf-ac70-8541ded5f005", BlockBytes: 262144,
		IndexPartition: "z", DataPartition: "q", Partitions: map[string]uint32{"q": 0, "z": 1}}
}

type ltfsLabelsProvider struct {
	*tarReadMapProvider
	readErr        error
	reserveErr     error
	reads, locates int
}

func (p *ltfsLabelsProvider) Open(identity tape.Identity) (tape.Transport, error) {
	transport, err := p.tarReadMapProvider.Open(identity)
	if err != nil {
		return nil, err
	}
	return &ltfsLabelsHandle{Transport: transport, provider: p}, nil
}

type ltfsLabelsHandle struct {
	tape.Transport
	provider        *ltfsLabelsProvider
	partition, step int
}

func (*ltfsLabelsHandle) TransferLimit() int { return 512 << 10 }

func (h *ltfsLabelsHandle) EnsureTransfer(ctx context.Context, _ int) error {
	return errors.Join(h.provider.reserveErr, ctx.Err())
}

func (h *ltfsLabelsHandle) Execute(ctx context.Context, command tape.Command) (tape.Response, error) {
	// Replace only the two physical Label prefixes; inspection, geometry and close use the real fixture path.
	if command.CDB[0] == 0x92 {
		h.provider.locates++
		h.partition, h.step = int(command.CDB[3]), 0
		return tape.Response{Completed: true}, nil
	}
	if command.CDB[0] != 0x08 {
		return h.Transport.Execute(ctx, command)
	}
	h.provider.reads++
	if h.provider.readErr != nil {
		return tape.Response{}, h.provider.readErr
	}

	// Signed ILI and explicit FILEMARK framing exercise production record decoding and XML parsing together.
	var value []byte
	switch h.step {
	case 0:
		value = make([]byte, 80)
		copy(value, "VOL1ABC001")
		copy(value[24:], "LTFS")
	case 2:
		partition := []string{"q", "z"}[h.partition]
		value = []byte(fmt.Sprintf(`<ltfslabel version="2.4.0"><creator>fixture</creator>`+
			`<formattime>2026-10-22T00:00:00.000000000Z</formattime>`+
			`<volumeuuid>204d1c2f-c755-44cf-ac70-8541ded5f005</volumeuuid>`+
			`<location><partition>%s</partition></location><partitions><index>z</index><data>q</data></partitions>`+
			`<blocksize>262144</blocksize><compression>false</compression></ltfslabel>`, partition))
	}
	h.step++
	remaining, flags, ascq := len(command.Data)-len(value), byte(0x20), byte(0)
	if value == nil {
		flags, ascq = 0x80, 1
	}
	copy(command.Data, value)
	return tape.Response{Completed: true, Status: 2, Residual: int32(remaining),
		Sense: tarFixtureSense(0, flags, ascq, int32(remaining))}, nil
}

func TestLTFSLabelObservationClosesBeforeNativeOwnership(t *testing.T) {
	for _, test := range []struct {
		name                          string
		readErr, closeErr, reserveErr error
	}{
		{name: "paired Labels"},
		{name: "optional unavailable", readErr: tape.ErrUnsupported},
		{name: "optional host reservation", reserveErr: errors.New("SG reserved buffer is too small")},
		{name: "optional absence and close failure", readErr: tape.ErrUnsupported, closeErr: errors.New("fixture close failed")},
		{name: "native failure", readErr: errors.New("fixture native READ failed")},
		{name: "canceled", readErr: context.Canceled},
		{name: "unknown completion", readErr: &tape.UncertainError{Cause: errors.New("fixture lost completion")}},
		{name: "observation and close failures", readErr: errors.New("fixture READ failure"), closeErr: errors.New("fixture close failure")},
	} {
		t.Run(test.name, func(t *testing.T) {
			// The same admitted attempt owns inspection, Label I/O and final descriptor release.
			exe := setupTestExecutor(t)
			fixture := &ltfsLabelsProvider{tarReadMapProvider: &tarReadMapProvider{
				tarReloadFixture: &tarReloadFixture{tarDeviceFixture: newTarDeviceFixture()},
				endpoints:        []tape.WrapEndpoint{{Partition: 0, Object: 99}, {Partition: 0, Wrap: 1, Object: 180}},
			}, readErr: test.readErr, reserveErr: test.reserveErr}
			fixture.cleanupError = test.closeErr
			exe.tapeProvider = fixture
			require.NoError(t, exe.beginAttempt(1, func(error) {}))
			t.Cleanup(func() { exe.endAttempt(1) })
			ctx := context.WithValue(t.Context(), deviceSettingsKey{}, &entity.DeviceSettings{
				Devices: []*entity.DeviceConfig{{Device: "/dev/nst0", Options: &entity.DeviceConfig_Tape{
					Tape: &entity.TapeDeviceSettings{ReadOrder: entity.TapeReadOrder_TAPE_READ_ORDER_AUTO},
				}}},
			})
			require.NoError(t, exe.AcquireTapeDevice(ctx, 1, "/dev/nst0", nil))
			backend := exe.NewMediaBackend(1, nil, nil).(*mediaBackend)
			snapshot, err := backend.observeLTFSReadOrder(ctx, "/dev/nst0", &entity.TapeMediaProfile{
				Format: library.TapeFormatLTFSV1,
			})
			require.Equal(t, 1, fixture.opens)
			require.Equal(t, 1, fixture.closes)
			require.Zero(t, fixture.writes)
			require.Empty(t, fixture.securityEvents)

			// Only a settled optional absence permits the later native mount; true failures retain their causes.
			if test.readErr != nil && !tape.CleanUnsupported(test.readErr) || test.closeErr != nil {
				require.Error(t, err)
				require.Nil(t, snapshot)
				if test.readErr != nil && !tape.CleanUnsupported(test.readErr) {
					require.ErrorIs(t, err, test.readErr)
				}
				if test.closeErr != nil {
					require.ErrorIs(t, err, test.closeErr)
				}
				return
			}
			require.NoError(t, err)
			if test.readErr == nil && test.reserveErr == nil {
				require.Equal(t, ltfsSGLabelFacts(), snapshot.labels)
				require.Equal(t, 2, fixture.locates)
				require.Equal(t, 8, fixture.reads)
			} else {
				require.Nil(t, snapshot.labels)
				require.Contains(t, snapshot.labelReason, "unavailable")
			}
			exe.scripts.Mount = writeExecutorTestScript(t, "mounted", ":")
			exe.scripts.Umount = writeExecutorTestScript(t, "unmounted", ":")
			point, err := backend.mountTape(ctx, "/dev/nst0", t.TempDir())
			require.NoError(t, err)
			require.Equal(t, fixture.opens, fixture.closes)
			require.NoError(t, backend.unmountTape(ctx, "/dev/nst0", point, t.TempDir()))
			require.Equal(t, 1, fixture.opens)
		})
	}
}

func TestLTFSReadFactsUseSGLabelsWithoutVendorAttributes(t *testing.T) {
	// HPE exposes the standard mounted facts but no IBM mediaMAM selectors.
	values, labels := ltfsAttributeFixture(), ltfsSGLabelFacts()
	var calls []string
	facts, err := readLTFSReadFacts(t.Context(), labels, func(name string, _ int) ([]byte, error) {
		calls = append(calls, name)
		if strings.HasPrefix(name, "ltfs.vendor.") {
			return nil, errors.New("vendor attribute unavailable")
		}
		return values[name], nil
	})
	require.NoError(t, err)
	require.Len(t, calls, 5)
	require.Equal(t, map[string]uint32{"q": 0, "z": 1}, facts.partitions)
	require.Equal(t, uint64(7), facts.generation)
	require.Equal(t, "pre-mount SG LTFS Labels", facts.partitionSource)

	// Request-owned facts must not let later consumers mutate the preparation snapshot.
	facts.partitions["q"] = 1
	require.Zero(t, labels.Partitions["q"])
}

func TestLTFSReadFactsRejectMountedLabelConflicts(t *testing.T) {
	for _, test := range []struct {
		name  string
		field string
		value string
	}{
		{"different volume", "volumeUUID", "e20588e2-9df7-4a45-99c8-229de7b08796"},
		{"different block size", "volumeBlocksize", "524288"},
		{"different role letters", "partitionMap", "I:a,D:b"},
		{"different role assignment", "partitionMap", "I:q,D:z"},
		{"written compression", "volumeCompression", "true"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Even usable mounted MAM evidence cannot override a contradictory pre-mount volume.
			values := ltfsAttributeFixture()
			values["ltfs."+test.field] = []byte(test.value)
			var calls int
			facts, err := readLTFSReadFacts(t.Context(), ltfsSGLabelFacts(), func(name string, _ int) ([]byte, error) {
				calls++
				return values[name], nil
			})
			require.Error(t, err)
			require.Nil(t, facts)
			require.Equal(t, 5, calls)
		})
	}
}

func TestLTFSReadOrderPreparedSessionUsesSGLabels(t *testing.T) {
	// Only standard mounted attributes are present; partition addresses must come from prepared SG facts.
	exe := setupTestExecutor(t)
	require.NoError(t, exe.Lib().AutoMigrate())
	stored, err := exe.Lib().CreateMedia(t.Context(), &library.Media{Kind: entity.MediaKind_MEDIA_KIND_TAPE,
		Identity: "ABC001", Profile: (&entity.TapeMediaProfile{Format: library.TapeFormatLTFSV1}).Pack()})
	require.NoError(t, err)
	point := t.TempDir()
	values := ltfsAttributeFixture()
	for _, name := range []string{"volumeBlocksize", "volumeCompression", "volumeUUID", "indexGeneration", "partitionMap", "indexLocation"} {
		attribute := "ltfs." + name
		if runtime.GOOS != "darwin" {
			attribute = "user." + attribute
		}
		if err := unix.Setxattr(point, attribute, values["ltfs."+name], 0); err != nil {
			t.Skipf("fixture filesystem does not support LTFS attributes: %v", err)
		}
	}
	_, report, _ := tarReadParametersFixture()
	report.GetTape().DensityCode = proto.Uint32(0x58)
	defaults := &entity.TapeDeviceSettings{ReadOrder: entity.TapeReadOrder_TAPE_READ_ORDER_AUTO}
	snapshot := &ltfsReadSnapshot{labels: ltfsSGLabelFacts(), report: report, defaults: defaults,
		boundary: entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_INCLUSIVE,
		settings: &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{Device: "/dev/nst0",
			Options: &entity.DeviceConfig_Tape{Tape: defaults}}}}, source: "A3/1F/45", observedAt: time.Now(),
		endpoints: []tape.WrapEndpoint{{Partition: 0, Object: 99}, {Partition: 0, Wrap: 1, Object: 180},
			{Partition: 1, Wrap: 4, Object: 8}}}
	session := &tapeReadSession{backend: exe.NewMediaBackend(1, nil, nil).(*mediaBackend),
		media: describeMedia(stored), device: "/dev/nst0", mountPoint: point}

	// The existing provider and Media map consume the source without changing the native source path.
	require.NoError(t, session.prepareReadOrder(t.Context(), stored, snapshot))
	provider, reason, err := session.ReadOrder(t.Context())
	require.NoError(t, err)
	require.NotNil(t, provider, reason)
	ranges, known := provider.Ranges(mediapkg.ReadRequest{Storage: &entity.StoragePosition{
		Metadata: (&entity.LtfsMetadata{Extents: []*entity.LtfsExtent{{Partition: "q", StartBlock: 10, ByteCount: 19}}}).Pack(),
	}})
	require.True(t, known)
	require.Zero(t, ranges[0].Start.Partition)
	updated, err := exe.Lib().GetMediaByIdentity(t.Context(), entity.MediaKind_MEDIA_KIND_TAPE, "ABC001")
	require.NoError(t, err)
	require.Contains(t, updated.TapePositionMap.Source, "pre-mount SG LTFS Labels")

	// The same Session retains the ordinary native source path passed to ACP.
	require.NoError(t, os.WriteFile(point+"/file", []byte("native"), 0600))
	source, err := session.Source(mediapkg.ReadRequest{Path: "file"})
	require.NoError(t, err)
	require.Nil(t, source.Content)
	require.NotEmpty(t, source.Path)
}

func TestLTFSReadFactsCancellationDoesNotConsumeMountedFacts(t *testing.T) {
	// Cancellation remains a real request outcome even with a complete pre-mount snapshot.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := readLTFSReadFacts(ctx, ltfsSGLabelFacts(), func(string, int) ([]byte, error) {
		t.Fatal("canceled preparation read mounted facts")
		return nil, nil
	})
	require.ErrorIs(t, err, context.Canceled)
}
