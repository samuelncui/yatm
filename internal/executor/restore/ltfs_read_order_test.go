package restore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type ltfsNativeRestoreSession struct {
	media.ReadSession
	provider media.ReadOrderProvider
	root     string
	resolved []string
}

func (*ltfsNativeRestoreSession) Capabilities() media.Capabilities {
	return media.Capabilities{Read: media.AccessSequential}
}

func (s *ltfsNativeRestoreSession) ReadOrder(context.Context) (media.ReadOrderProvider, string, error) {
	return s.provider, "", nil
}

func (s *ltfsNativeRestoreSession) Source(request media.ReadRequest) (media.SourceRef, error) {
	s.resolved = append(s.resolved, request.Path)
	return media.SourceRef{Path: filepath.Join(s.root, request.Path)}, nil
}

func TestRestoreConsumesLTFSProviderSequenceAsNativePaths(t *testing.T) {
	for _, batch := range []int{1, 3} {
		for _, current := range []bool{false, true} {
			t.Run(strconv.Itoa(batch)+"/current-index="+strconv.FormatBool(current), func(t *testing.T) {
				// The real LTFS provider receives frozen fragmented extents; ACP still opens ordinary native files.
				files := map[string][]byte{"a": []byte("aa"), "b": []byte("b"), "c": []byte("c"), "d": []byte("d")}
				runner, _, copies := setupVolumeRestore(t, "ltfs-native", files)
				session := &ltfsNativeRestoreSession{root: t.TempDir()}
				starts := map[string]uint64{"a": 10, "b": 20, "c": 80, "d": 120}
				index := `<ltfsindex><volumeuuid>204d1c2f-c755-44cf-ac70-8541ded5f005</volumeuuid><generationnumber>7</generationnumber><directory><name>TAPE</name><contents>`
				for _, copy := range copies {
					metadata := &entity.LtfsMetadata{Extents: []*entity.LtfsExtent{{Partition: "q",
						StartBlock: starts[copy.MediaPath], ByteCount: 1}}}
					if copy.MediaPath == "a" {
						metadata.Extents = []*entity.LtfsExtent{{Partition: "q", StartBlock: 10, ByteCount: 1},
							{Partition: "q", StartBlock: 79, ByteCount: 1, FileOffsetBytes: 1}}
					}
					row := Copy{StorageOrder: []byte{byte(starts[copy.MediaPath])}, StorageMetadata: metadata.Pack()}
					index += `<file><name>` + copy.MediaPath + `</name><length>` + strconv.Itoa(len(files[copy.MediaPath])) + `</length><extentinfo>`
					for _, extent := range metadata.Extents {
						index += `<extent><partition>q</partition><startblock>` + strconv.FormatUint(extent.StartBlock, 10) + `</startblock><bytecount>` + strconv.FormatUint(extent.ByteCount, 10) + `</bytecount><fileoffset>` + strconv.FormatUint(extent.FileOffsetBytes, 10) + `</fileoffset></extent>`
					}
					index += `</extentinfo></file>`
					if current {
						row.StorageMetadata = nil
					}
					require.NoError(t, runner.db.Model(&Copy{}).Where("id = ?", copy.ID).
						Select("storage_order", "storage_metadata").Updates(&row).Error)
					require.NoError(t, os.WriteFile(filepath.Join(session.root, copy.MediaPath), files[copy.MediaPath], 0600))
				}
				index += `</contents></directory></ltfsindex>`
				model, err := media.NewReadCostModel(media.ReadModelProfile{ID: "LTFS Restore fixture",
					SnapshotValid: func() bool { return true }, Boundary: media.ReadEndpointExclusive,
					BOT: map[uint32]uint64{1: 0}, FullWrapSeconds: proto.Float64(1000),
					Wraps: []media.ReadModelWrap{
						{Partition: 1, Wrap: 0, Full: true, Endpoint: proto.Uint64(100), Direction: media.ReadWrapForward},
						{Partition: 1, Wrap: 1, Full: true, Endpoint: proto.Uint64(200), Direction: media.ReadWrapReverse},
					}})
				require.NoError(t, err)
				var reason string
				indexReads := 0
				session.provider, reason = media.NewLTFSReadOrderProvider(media.LTFSReadOptions{BlockBytes: 262144,
					Compression: proto.Bool(false), Partitions: map[string]uint32{"q": 1}, Model: model,
					Completion: &media.ReadCompletion{FullSpanSeconds: 1000},
					DataEnd:    &entity.TapeBytePosition{Partition: 1, LogicalObject: 200},
					ReadIndex: func(ctx context.Context, yield func(*media.LTFSIndexEntry) error) error {
						indexReads++
						return media.ParseLTFSReadIndex(ctx, strings.NewReader(index), "204d1c2f-c755-44cf-ac70-8541ded5f005", 7, yield)
					},
					Timing: &entity.TapeReadTiming{BytesPerSecond: proto.Float64(1e9),
						PostLocateReadOverheadSeconds: proto.Float64(0)}})
				require.NotNil(t, session.provider, reason)

				// The production Restore feeder submits the same complete plan across different ACP batch sizes.
				buffer := startTestBuffer(t, runner)
				items := &copyItems{runner: runner, mediaID: copies[0].MediaID, session: session, batch: batch}
				engine, err := acp.NewStream(t.Context(), buffer.onResults, acp.WithHashPolicy(acp.HashReadRefresh),
					acp.Overwrite(false), acp.SetFromDevice(media.DeviceOptions(media.AccessSequential)...),
					acp.WithResultBatch(100), acp.WithResultBuffer(100), acp.WithResultFlushInterval(time.Hour))
				require.NoError(t, err)
				require.NoError(t, errors.Join(items.consume(t.Context(), engine), buffer.Close()))
				require.Equal(t, []string{"a", "c", "d", "b"}, session.resolved)
				if current {
					require.Equal(t, 1, indexReads)
					var unchanged []Copy
					require.NoError(t, runner.db.Find(&unchanged).Error)
					for _, copy := range unchanged {
						require.Nil(t, copy.StorageMetadata, "live extents do not rewrite the frozen Job")
					}
				} else {
					require.Zero(t, indexReads, "complete saved extents avoid reading the current Index")
				}

				// Successful native transfers retain each requested hash/output and await ordinary Media finalization.
				var outputs []File
				require.NoError(t, runner.db.Find(&outputs).Error)
				require.Len(t, outputs, 4)
				for _, output := range outputs {
					require.True(t, output.Ready)
					require.False(t, output.Completed)
				}
				for _, copy := range copies {
					actual, err := os.ReadFile(runner.restoreTarget(copy.TargetPath))
					require.NoError(t, err)
					require.Equal(t, files[copy.MediaPath], actual)
				}
			})
		}
	}
}

func (*ltfsNativeRestoreSession) ReadOrderPlanned(entity.TapeReadOrder, string) {}
