package executor

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/readplan"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"
)

func ltfsMAMFixture(capacity, indexBlock uint64) []byte {
	// SSC 0001 is partition maximum capacity; LTFS 080c coherency retains the partition's Index block.
	value := make([]byte, 4+5+8+5+70)
	binary.BigEndian.PutUint32(value, uint32(len(value)-4))
	binary.BigEndian.PutUint16(value[4:], 0x0001)
	binary.BigEndian.PutUint16(value[7:], 8)
	binary.BigEndian.PutUint64(value[9:], capacity)
	coherency := value[17:]
	binary.BigEndian.PutUint16(coherency, 0x080c)
	binary.BigEndian.PutUint16(coherency[3:], 70)
	coherency[5] = 8
	binary.BigEndian.PutUint64(coherency[6:], 12)
	binary.BigEndian.PutUint64(coherency[14:], 7)
	binary.BigEndian.PutUint64(coherency[22:], indexBlock)
	binary.BigEndian.PutUint16(coherency[30:], 43)
	copy(coherency[32:], "LTFS\x00")
	copy(coherency[37:], "204d1c2f-c755-44cf-ac70-8541ded5f005\x00")
	coherency[74] = 1
	return value
}

func ltfsAttributeFixture() map[string][]byte {
	index, data := ltfsMAMFixture(37500, 8), ltfsMAMFixture(1425000, 180)
	return map[string][]byte{
		"ltfs.volumeBlocksize": []byte("262144"), "ltfs.volumeCompression": []byte("false"),
		"ltfs.volumeUUID": []byte("204d1c2f-c755-44cf-ac70-8541ded5f005"), "ltfs.indexGeneration": []byte("7"),
		"ltfs.indexLocation":          []byte("z:8"),
		"ltfs.partitionMap":           []byte("I:z,D:q"),
		"ltfs.vendor.IBM.mediaMAM.IP": index, "ltfs.vendor.IBM.mediaMAM.DP": data,
		"ltfs.vendor.IBM.mediaMAM.00": data, "ltfs.vendor.IBM.mediaMAM.01": index,
	}
}

func TestLTFSReadOrderFactsUseLabelAndNumericPartitionEvidence(t *testing.T) {
	// Reversed physical roles and arbitrary symbols must not acquire the common a=0/b=1 assumption.
	values := ltfsAttributeFixture()
	var calls []string
	facts, err := readLTFSReadFacts(t.Context(), nil, func(name string, limit int) ([]byte, error) {
		calls = append(calls, name)
		require.LessOrEqual(t, len(values[name]), limit)
		return values[name], nil
	})
	require.NoError(t, err)
	require.Equal(t, uint32(262144), facts.blockBytes)
	require.False(t, facts.compression)
	require.Equal(t, uint64(7), facts.generation)
	require.Equal(t, map[string]uint32{"q": 0, "z": 1}, facts.partitions)
	require.Equal(t, "z", facts.indexSymbol)
	require.Equal(t, "q", facts.dataSymbol)
	require.Len(t, calls, 9)
}

func TestLTFSReadOrderFactsRejectAmbiguousAndUnsupportedObservations(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(map[string][]byte)
		want string
	}{
		{"compressed label", func(v map[string][]byte) { v["ltfs.volumeCompression"] = []byte("true") }, "compression"},
		{"missing compression", func(v map[string][]byte) { delete(v, "ltfs.volumeCompression") }, "compression"},
		{"block size absent", func(v map[string][]byte) { delete(v, "ltfs.volumeBlocksize") }, "block size"},
		{"bad UUID", func(v map[string][]byte) { v["ltfs.volumeUUID"] = []byte("barcode") }, "UUID"},
		{"truncated MAM", func(v map[string][]byte) { v["ltfs.vendor.IBM.mediaMAM.IP"] = []byte{0, 0, 0, 5} }, "incomplete"},
		{"identical physical snapshots", func(v map[string][]byte) {
			v["ltfs.vendor.IBM.mediaMAM.01"] = v["ltfs.vendor.IBM.mediaMAM.00"]
		}, "indistinguishable"},
		{"equal partition capacities with differing other attributes", func(v map[string][]byte) {
			v["ltfs.vendor.IBM.mediaMAM.00"] = ltfsMAMFixture(37500, 180)
			v["ltfs.vendor.IBM.mediaMAM.DP"] = v["ltfs.vendor.IBM.mediaMAM.00"]
		}, "indistinguishable"},
		{"changed role observation", func(v map[string][]byte) {
			v["ltfs.vendor.IBM.mediaMAM.DP"] = ltfsMAMFixture(1425000, 181)
		}, "do not match"},
		{"two roles claim same physical partition", func(v map[string][]byte) {
			v["ltfs.vendor.IBM.mediaMAM.DP"] = v["ltfs.vendor.IBM.mediaMAM.IP"]
		}, "one-to-one"},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := ltfsAttributeFixture()
			test.edit(values)
			_, err := readLTFSReadFacts(t.Context(), nil, func(name string, _ int) ([]byte, error) { return values[name], nil })
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestLTFSReadOrderObservationClosesRawAccessBeforeMount(t *testing.T) {
	for _, format := range []string{library.TapeFormatLTFSV0, library.TapeFormatLTFSV1} {
		t.Run(format, func(t *testing.T) {
			// Reuse the public-protocol fixture to observe real inspection parsing without a physical device.
			exe := setupTestExecutor(t)
			fixture := &tarReadMapProvider{tarReloadFixture: &tarReloadFixture{tarDeviceFixture: newTarDeviceFixture()},
				endpoints: []tape.WrapEndpoint{{Partition: 0, Object: 99}, {Partition: 0, Wrap: 1, Object: 180}}}
			exe.tapeProvider = fixture
			require.NoError(t, exe.beginAttempt(1, func(error) {}))
			t.Cleanup(func() { exe.endAttempt(1) })
			ctx := context.WithValue(t.Context(), deviceSettingsKey{}, &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{
				Device: "/dev/nst0", Options: &entity.DeviceConfig_Tape{Tape: &entity.TapeDeviceSettings{
					ReadOrder: entity.TapeReadOrder_TAPE_READ_ORDER_AUTO,
				}},
			}}})
			require.NoError(t, exe.AcquireTapeDevice(ctx, 1, "/dev/nst0", nil))
			backend := exe.NewMediaBackend(1, nil, nil).(*mediaBackend)
			snapshot, err := backend.observeLTFSReadOrder(ctx, "/dev/nst0", &entity.TapeMediaProfile{Format: format})
			require.NoError(t, err)
			require.NotNil(t, snapshot.report)
			require.Equal(t, fixture.endpoints, snapshot.endpoints)
			require.Equal(t, 1, fixture.opens)
			require.Equal(t, 1, fixture.closes)
			require.Empty(t, fixture.locateCommands)
			require.Zero(t, fixture.cursor)
			require.Zero(t, fixture.writes)

			// Native ownership begins only after the optional descriptor has closed; later use consumes copied facts.
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

func TestLTFSReadOrderPreparedSessionPublishesUsableNativeProvider(t *testing.T) {
	for _, format := range []string{library.TapeFormatLTFSV0, library.TapeFormatLTFSV1} {
		t.Run(format, func(t *testing.T) {
			// A mounted filesystem supplies label/MAM facts through its normal xattr interface.
			exe := setupTestExecutor(t)
			require.NoError(t, exe.Lib().AutoMigrate())
			stored, err := exe.Lib().CreateMedia(t.Context(), &library.Media{Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "ABC001",
				Profile: (&entity.TapeMediaProfile{Format: format}).Pack()})
			require.NoError(t, err)
			point := t.TempDir()
			for name, value := range ltfsAttributeFixture() {
				if runtime.GOOS != "darwin" {
					name = "user." + name
				}
				if err := unix.Setxattr(point, name, value, 0); err != nil {
					t.Skipf("fixture filesystem does not support LTFS attribute fixtures: %v", err)
				}
			}
			path := filepath.Join(point, "native name.txt")
			require.NoError(t, os.WriteFile(path, []byte("actual native bytes"), 0600))
			_, report, _ := tarReadParametersFixture()
			report.GetTape().DensityCode = proto.Uint32(0x58)
			defaults := &entity.TapeDeviceSettings{ReadOrder: entity.TapeReadOrder_TAPE_READ_ORDER_AUTO}
			snapshot := &ltfsReadSnapshot{report: report, defaults: defaults,
				boundary: entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_INCLUSIVE,
				settings: &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{Device: "/dev/nst0",
					Options: &entity.DeviceConfig_Tape{Tape: defaults}}}},
				endpoints: []tape.WrapEndpoint{{Partition: 0, Object: 99}, {Partition: 0, Wrap: 1, Object: 180},
					{Partition: 1, Wrap: 4, Object: 8}}, source: "A3/1F/45", observedAt: time.Now()}
			exe.scripts.Umount = writeExecutorTestScript(t, "unmounted", ":")
			session := &tapeReadSession{backend: exe.NewMediaBackend(1, nil, nil).(*mediaBackend), media: describeMedia(stored),
				device: "/dev/nst0", mountPoint: point, tapeDir: t.TempDir(), recycleKey: func() {}}
			session.indexPath = filepath.Join(session.tapeDir, "ABC001.schema")
			require.NoError(t, session.prepareReadOrder(t.Context(), stored, snapshot))
			provider, reason, err := session.ReadOrder(t.Context())
			require.NoError(t, err)
			require.NotNil(t, provider, reason)
			request := mediapkg.ReadRequest{Path: "native name.txt", Storage: &entity.StoragePosition{Metadata: (&entity.LtfsMetadata{
				Extents: []*entity.LtfsExtent{{Partition: "q", StartBlock: 10, ByteCount: 19}},
			}).Pack()}}
			ranges, known := provider.Ranges(request)
			require.True(t, known)
			require.Equal(t, uint32(0), ranges[0].Start.Partition)
			_, known = provider.(readplan.Provider).Finish(readplan.State{
				Position: &entity.TapeBytePosition{LogicalObject: 11}, Stream: "ltfs",
			})
			require.True(t, known, "the prepared provider must price its normal terminal action")
			source, err := session.Source(request)
			require.NoError(t, err)
			native, err := filepath.EvalSymlinks(path)
			require.NoError(t, err)
			require.Equal(t, native, source.Path)
			require.Nil(t, source.Content)

			// Missing historical extents are supplied from this mount's captured Index, with matching UUID/generation.
			index := `<ltfsindex><volumeuuid>204d1c2f-c755-44cf-ac70-8541ded5f005</volumeuuid><generationnumber>7</generationnumber><directory><name>ABC001</name><contents>`
			for i, name := range []string{"native name.txt", "another.txt"} {
				index += fmt.Sprintf(`<file><name>%s</name><length>19</length><extentinfo><extent><partition>q</partition><startblock>%d</startblock><bytecount>19</bytecount></extent></extentinfo></file>`, name, 10+i*100)
			}
			index += `</contents></directory></ltfsindex>`
			require.NoError(t, os.WriteFile(session.indexPath, []byte(index), 0600))
			input := iter.Seq2[readplan.Span, error](func(yield func(readplan.Span, error) bool) {
				if yield(readplan.Span{ID: 1, Path: "native name.txt", Size: 19}, nil) {
					yield(readplan.Span{ID: 2, Path: "another.txt", Size: 19}, nil)
				}
			})
			sequence, err := readplan.Plan(t.Context(), t.TempDir(), provider, input)
			require.NoError(t, err)
			require.NotContains(t, sequence.Reason(), "incomplete")
			require.NotContains(t, sequence.Reason(), "unavailable")
			ids, err := sequence.Next(t.Context(), 10)
			require.NoError(t, err)
			require.Equal(t, []int64{1, 2}, ids, "the unknown initial position keeps the common first file")
			require.NoError(t, sequence.Close())

			// The published map has the mounted UUID/index owner, and finalization withdraws that snapshot.
			updated, err := exe.Lib().GetMediaByIdentity(t.Context(), entity.MediaKind_MEDIA_KIND_TAPE, "ABC001")
			require.NoError(t, err)
			require.Equal(t, "204d1c2f-c755-44cf-ac70-8541ded5f005", updated.TapePositionMap.LtfsUuid)
			require.Equal(t, uint64(7), updated.TapePositionMap.GetLtfsIndexGeneration())
			require.Equal(t, entity.TapeReadOrder_TAPE_READ_ORDER_AUTO.String(), exe.lastPreparedRead("/dev/nst0").EffectiveOrder)
			require.Equal(t, "builtin", exe.lastPreparedRead("/dev/nst0").ParametersSource)
			require.Contains(t, exe.lastPreparedRead("/dev/nst0").ParametersReference, "unmeasured")
			require.NoError(t, session.Finalize(t.Context(), nil))
			_, known = provider.Ranges(request)
			require.False(t, known)
			provider, reason, err = session.ReadOrder(t.Context())
			require.NoError(t, err)
			require.Nil(t, provider)
			require.Contains(t, reason, "finalized")
		})
	}
}

func TestLTFSReadOrderOptionalObservationFailurePreservesNativeSession(t *testing.T) {
	// Missing optional native facts disable the provider; they do not replace or wrap the ordinary source.
	point := t.TempDir()
	path := filepath.Join(point, "file")
	require.NoError(t, os.WriteFile(path, []byte("native"), 0600))
	exe := New(nil, nil, []string{"fixture"}, Paths{}, Scripts{}, nil)
	stored := &library.Media{ID: 1, Identity: "ABC001", Profile: (&entity.TapeMediaProfile{Format: library.TapeFormatLTFSV1}).Pack()}
	session := &tapeReadSession{backend: exe.NewMediaBackend(1, nil, nil).(*mediaBackend),
		media: describeMedia(stored), mountPoint: point, device: "fixture"}
	snapshot := &ltfsReadSnapshot{defaults: &entity.TapeDeviceSettings{ReadOrder: entity.TapeReadOrder_TAPE_READ_ORDER_AUTO}}
	require.NoError(t, session.prepareReadOrder(t.Context(), stored, snapshot))
	provider, reason, err := session.ReadOrder(t.Context())
	require.NoError(t, err)
	require.Nil(t, provider)
	require.Contains(t, reason, "volumeBlocksize")
	source, err := session.Source(mediapkg.ReadRequest{Path: "file"})
	require.NoError(t, err)
	native, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)
	require.Equal(t, native, source.Path)
	require.Nil(t, source.Content)

	// Cancellation keeps its identity rather than becoming an optional metadata miss.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = session.ReadOrder(ctx)
	require.True(t, errors.Is(err, context.Canceled))
}
