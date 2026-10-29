package scan

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/readplan"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type currentIndexScanSession struct {
	*orderedScanSession
	provider media.ReadOrderProvider
}

func (s *currentIndexScanSession) ReadOrder(context.Context) (media.ReadOrderProvider, string, error) {
	return s.provider, "", nil
}

func TestScanCurrentIndexCompletesPreboundNativeRanges(t *testing.T) {
	for _, batch := range []int{1, 3} {
		for _, stale := range []bool{false, true} {
			t.Run(fmt.Sprintf("batch=%d/stale=%t", batch, stale), func(t *testing.T) {
				// The real preparation path binds native sources before any missing-extent lookup.
				r, rows, base := scanOrderFixture(t, "a", "b", "c", "d")
				session := &currentIndexScanSession{orderedScanSession: base}
				root := t.TempDir()
				starts := []int{10, 20, 80, 120}
				index := `<ltfsindex><volumeuuid>volume</volumeuuid><generationnumber>7</generationnumber><directory><name>TAPE</name><contents>`
				for i, row := range rows {
					content := []byte(row.Path)
					if row.Path == "a" {
						content = []byte("aa")
					}
					filename := filepath.Join(root, row.Path)
					require.NoError(t, os.WriteFile(filename, content, 0600))
					base.native[row.Path] = filename
					hash := sha256.Sum256(content)
					row.Size, row.Expected.SizeBytes, row.Expected.Sha256 = int64(len(content)), int64(len(content)), hash[:]
					row.Storage, row.StorageOrder = nil, []byte{byte(i + 1)}
					require.NoError(t, r.db.Save(row).Error)
					index += fmt.Sprintf(`<file><name>%s</name><length>%d</length><extentinfo><extent><partition>q</partition><startblock>%d</startblock><bytecount>1</bytecount></extent>`, row.Path, len(content), starts[i])
					if row.Path == "a" {
						index += `<extent><partition>q</partition><startblock>79</startblock><bytecount>1</bytecount><fileoffset>1</fileoffset></extent>`
					}
					index += `</extentinfo></file>`
				}
				index += `</contents></directory></ltfsindex>`
				if stale {
					index = strings.Replace(index, ">7<", ">6<", 1)
				}
				require.NoError(t, r.resolveMediaSources(t.Context(), session))

				// Use the production LTFS state model and parser, including the common first file and unload cost.
				model, err := media.NewReadCostModel(media.ReadModelProfile{ID: "Scan current Index fixture",
					SnapshotValid: func() bool { return true }, Boundary: media.ReadEndpointExclusive,
					BOT: map[uint32]uint64{1: 0}, FullWrapSeconds: proto.Float64(1000),
					Wraps: []media.ReadModelWrap{
						{Partition: 1, Wrap: 0, Full: true, Endpoint: proto.Uint64(100), Direction: media.ReadWrapForward},
						{Partition: 1, Wrap: 1, Full: true, Endpoint: proto.Uint64(200), Direction: media.ReadWrapReverse},
					}})
				require.NoError(t, err)
				indexReads := 0
				var reason string
				session.provider, reason = media.NewLTFSReadOrderProvider(media.LTFSReadOptions{
					BlockBytes: 512 << 10, Compression: proto.Bool(false), Partitions: map[string]uint32{"q": 1},
					Model: model, Completion: &media.ReadCompletion{FullSpanSeconds: 1000},
					DataEnd: &entity.TapeBytePosition{Partition: 1, LogicalObject: 200},
					Timing: &entity.TapeReadTiming{BytesPerSecond: proto.Float64(1e9),
						PostLocateReadOverheadSeconds: proto.Float64(0)},
					ReadIndex: func(ctx context.Context, yield func(*media.LTFSIndexEntry) error) error {
						indexReads++
						return media.ParseLTFSReadIndex(ctx, strings.NewReader(index), "volume", 7, yield)
					},
				})
				require.NotNil(t, session.provider, reason)

				// The ordinary feeder consumes the generated order as unchanged native paths and verifies all bytes.
				config := &Config{Spec: &entity.ScanJobSpec{MediaId: 1,
					SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ,
					ResultPolicy:    entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES}}
				stream, err := openTestContentStream(t, t.Context(), r, config, &Scope{}, session, flushSettings(batch, 100, 3600000))
				require.NoError(t, err)
				engine, err := acp.NewStream(t.Context(), stream.onResults, acp.WithHashPolicy(acp.HashRead),
					acp.SetFromDevice(media.DeviceOptions(media.AccessSequential)...), acp.WithResultBatch(100),
					acp.WithResultBuffer(100), acp.WithResultFlushInterval(time.Hour))
				require.NoError(t, err)
				var submitted []string
				runErr := executor.RunCopyPages(t.Context(), engine, func(ctx context.Context) ([]acp.Item, error) {
					items, err := stream.nextPage(ctx)
					for _, item := range items {
						submitted = append(submitted, filepath.Base(item.Source()))
						require.Nil(t, item.(*contentItem).ContentSource(), "native execution must not acquire an endpoint")
					}
					return items, err
				})
				require.NoError(t, errors.Join(runErr, stream.writer.Close()))
				require.NotNil(t, stream.sequence)
				require.NoError(t, stream.sequence.Close())
				requireScanReadSequenceCleanup(t, stream)

				// A stale Index retains every member in LOGICAL order; usable ranges change only execution ranks.
				want := []string{"a", "c", "d", "b"}
				if stale {
					want = []string{"a", "b", "c", "d"}
				}
				require.Equal(t, 1, indexReads)
				require.Equal(t, want, submitted)
				require.False(t, stream.trackReads)
				var stored []Entry
				require.NoError(t, r.db.Order("id").Find(&stored).Error)
				require.Len(t, stored, 4)
				for _, row := range stored {
					require.Equal(t, base.native[row.Path], row.SourcePath)
					require.Nil(t, row.Storage, "current Index cannot rewrite frozen storage")
					require.Equal(t, entity.ScanFinding_SCAN_FINDING_MATCH, row.Finding)
					require.Equal(t, row.Expected.Sha256, row.ActualHash)
				}
			})
		}
	}
}

func TestScanCurrentIndexSpanKeepsEmptyNativeSize(t *testing.T) {
	// Zero is an explicit frozen size, not an absent value or a reason to remove the current-Index lookup key.
	r, rows, session := scanOrderFixture(t, "empty")
	session.available = false
	row := rows[0]
	row.Size, row.SourcePath = 0, filepath.Join(t.TempDir(), "empty")
	require.NoError(t, os.WriteFile(row.SourcePath, nil, 0600))
	require.NoError(t, r.db.Save(row).Error)
	stream := &contentStream{runner: r, scope: &Scope{}, session: session, provider: session}
	var spans []readplan.Span
	for span, err := range stream.readSpans(t.Context()) {
		require.NoError(t, err)
		spans = append(spans, span)
	}
	require.Len(t, spans, 1)
	require.Equal(t, "empty", spans[0].Path)
	require.EqualValues(t, 0, spans[0].Size)
	require.Empty(t, spans[0].Ranges)
}

func (*currentIndexScanSession) ReadOrderPlanned(entity.TapeReadOrder, string) {}
