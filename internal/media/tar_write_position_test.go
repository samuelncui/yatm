package media

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/stretchr/testify/require"
)

type tarWritePositionDevice struct {
	*tarTestDevice
	start         tape.Position
	bits          uint32
	queries       int
	zeroWarningAt int
	observe       func(int, tape.Position) (tape.Position, error)
}

func (d *tarWritePositionDevice) Write(ctx context.Context, data []byte) (tape.RecordResult, error) {
	if d.writes+1 == d.zeroWarningAt {
		d.writes++
		return tape.RecordResult{AcceptedKnown: true, EarlyWarning: true}, tape.ErrEarlyWarning
	}
	return d.tarTestDevice.Write(ctx, data)
}

func (d *tarWritePositionDevice) Position(context.Context) (tape.Position, error) {
	d.queries++
	position := d.start
	position.Object += uint64(len(d.records))
	position.File += uint64(d.marks)
	if d.observe != nil {
		return d.observe(d.queries, position)
	}
	return position, nil
}

func (*tarWritePositionDevice) Locate(context.Context, uint32, uint64) error {
	return errors.New("writer must not locate")
}

func (d *tarWritePositionDevice) AddressBits() uint32 { return d.bits }

type tarWritePositionInfo struct{ tarTestInfo }

func (tarWritePositionInfo) ModTime() time.Time { return time.Unix(1000, 0) }

func newTarWritePositionDevice() *tarWritePositionDevice {
	return &tarWritePositionDevice{tarTestDevice: &tarTestDevice{}, bits: 64,
		start: tape.Position{Object: 100, File: 1, ObjectKnown: true, FileKnown: true}}
}

func writeTarPositionMember(t *testing.T, session WriteSession, name string, size int) {
	t.Helper()
	ref, err := session.Target(name, int64(size))
	require.NoError(t, err)
	writer, err := ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarWritePositionInfo{tarTestInfo{name: name, size: int64(size)}}})
	require.NoError(t, err)
	_, err = writer.Write(make([]byte, size))
	require.NoError(t, err)
	require.NoError(t, writer.Finish(nil))
}

func TestTarWritePositionsTrackPackedRecordsAndFinalShort(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "variable", true: "fixed"}[fixed], func(t *testing.T) {
			// Two members share one physical record; position queries occur only at its safe boundaries.
			device := newTarWritePositionDevice()
			options := tarTestOptions(t, fixed, 4096)
			options.Navigator = device
			session, err := newTarTestWriteSession(device, options)
			require.NoError(t, err)
			require.Zero(t, device.queries)
			writeTarPositionMember(t, session, "first", 1)
			writeTarPositionMember(t, session, "second", 1)
			require.Equal(t, 1, device.queries)
			result, err := session.Finalize(context.Background(), nil)
			require.NoError(t, err)

			// A variable final record counts as one object, independent of its shorter byte length.
			files := tarTestCompleted(t, session, result)
			require.Len(t, files, 2)
			require.Equal(t, 2, device.queries)
			require.Len(t, device.records, 2)
			recordBytes := 3072
			if fixed {
				recordBytes = 4096
			}
			require.Len(t, device.records[0].data, recordBytes)
			for index, file := range files {
				locator := file.Storage.Metadata.GetTar()
				require.Equal(t, &entity.TapeBytePosition{LogicalObject: 100, OffsetBytes: uint32(index * 1024)}, locator.Header)
				require.Equal(t, &entity.TapeBytePosition{LogicalObject: 100, OffsetBytes: uint32((index + 1) * 1024)}, locator.End)
				require.NoError(t, entity.ValidateTarMetadata(locator, 4096))
			}
		})
	}
}

func TestTarWritePositionEndAtRecordBoundaryAndLongPAX(t *testing.T) {
	for _, name := range []string{"boundary", strings.Repeat("long/", 30) + "member"} {
		t.Run(name, func(t *testing.T) {
			// Small records force the header/PAX chain and exclusive end across accepted object boundaries.
			device := newTarWritePositionDevice()
			options := tarTestOptions(t, false, 512)
			options.Navigator = device
			session, err := newTarTestWriteSession(device, options)
			require.NoError(t, err)
			writeTarPositionMember(t, session, name, 1)
			memberRecords := len(device.records)
			result, err := session.Finalize(context.Background(), nil)
			require.NoError(t, err)
			files := tarTestCompleted(t, session, result)
			require.Len(t, files, 1)
			locator := files[0].Storage.Metadata.GetTar()
			require.Equal(t, &entity.TapeBytePosition{LogicalObject: 100}, locator.Header)
			require.Equal(t, &entity.TapeBytePosition{LogicalObject: 100 + uint64(memberRecords)}, locator.End)
			require.Equal(t, 2, device.queries)
			require.NoError(t, entity.ValidateTarMetadata(locator, 512))
		})
	}
}

func TestTarWriteOptionalPositionUnknownOmitsPairedAnchors(t *testing.T) {
	for _, query := range []int{1, 2} {
		for _, unsupported := range []bool{false, true} {
			t.Run(fmt.Sprintf("query%d/%s", query, map[bool]string{false: "GOOD unknown", true: "unsupported"}[unsupported]), func(t *testing.T) {
				// Clean optional-query absence retains independently proven sequential placement.
				device := newTarWritePositionDevice()
				device.observe = func(call int, position tape.Position) (tape.Position, error) {
					if call == query {
						if unsupported {
							return tape.Position{}, tape.ErrUnsupported
						}
						position.ObjectKnown = false
					}
					return position, nil
				}
				options := tarTestOptions(t, false, 4096)
				options.Navigator = device
				session, err := newTarTestWriteSession(device, options)
				require.NoError(t, err)
				writeTarPositionMember(t, session, "sequential", 1)
				result, err := session.Finalize(context.Background(), nil)
				require.NoError(t, err)
				files := tarTestCompleted(t, session, result)
				require.Len(t, files, 1)
				locator := files[0].Storage.Metadata.GetTar()
				require.Equal(t, query, device.queries)
				require.Nil(t, locator.Header)
				require.Nil(t, locator.End)
				require.EqualValues(t, 1, locator.TapeFile)
				require.EqualValues(t, 0, locator.HeaderOffsetBytes)
			})
		}
	}
}

func TestTarWritePositionContradictionsNeverPublish(t *testing.T) {
	for name, change := range map[string]func(tape.Position) (tape.Position, error){
		"partition":    func(p tape.Position) (tape.Position, error) { p.Partition = 1; return p, nil },
		"file":         func(p tape.Position) (tape.Position, error) { p.File++; return p, nil },
		"object reset": func(p tape.Position) (tape.Position, error) { p.Object = 0; return p, nil },
		"beginning":    func(p tape.Position) (tape.Position, error) { p.Beginning = true; return p, nil },
		"malformed":    func(p tape.Position) (tape.Position, error) { return p, tape.ErrMalformed },
		"uncertain":    func(p tape.Position) (tape.Position, error) { return p, tape.ErrUncertain },
		"mixed unsupported": func(p tape.Position) (tape.Position, error) {
			return p, errors.Join(tape.ErrUnsupported, errors.New("drive failure"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			// A completed mark is insufficient when the subsequent exact position contradicts the write.
			device := newTarWritePositionDevice()
			device.observe = func(call int, p tape.Position) (tape.Position, error) {
				if call == 2 {
					return change(p)
				}
				return p, nil
			}
			options := tarTestOptions(t, false, 4096)
			options.Navigator = device
			session, err := newTarTestWriteSession(device, options)
			require.NoError(t, err)
			writeTarPositionMember(t, session, "member", 1)
			result, err := session.Finalize(context.Background(), nil)
			require.Nil(t, result)
			require.ErrorIs(t, err, ErrFinalizeUnusable)
			require.Equal(t, 1, device.marks)
		})
	}
}

func TestTarWritePositionWidthAndPreparedStop(t *testing.T) {
	for _, bits := range []uint32{32, 64} {
		t.Run(fmt.Sprintf("%d-bit", bits), func(t *testing.T) {
			// The address of the synchronized next Tape file must never wrap the provider's width.
			device := newTarWritePositionDevice()
			device.bits = bits
			device.start.Object = math.MaxUint64
			if bits == 32 {
				device.start.Object = math.MaxUint32
			}
			options := tarTestOptions(t, false, 512)
			options.Navigator = device
			session, err := newTarTestWriteSession(device, options)
			require.NoError(t, err)
			writeTarPositionMember(t, session, "sequential", 1)
			result, err := session.Finalize(context.Background(), nil)
			require.NoError(t, err)
			files := tarTestCompleted(t, session, result)
			require.Len(t, files, 1)
			require.Nil(t, files[0].Storage.Metadata.GetTar().Header)
			require.Nil(t, files[0].Storage.Metadata.GetTar().End)
			require.Equal(t, 1, device.queries)
			require.Equal(t, 4, device.writes)
			require.Equal(t, 1, device.marks)
		})
	}

	// A label EW already completed its boundary; no position query or content command follows it.
	device := newTarWritePositionDevice()
	options := tarTestOptions(t, false, 4096)
	options.Navigator = device
	options.Stop = errors.Join(ErrTargetNoSpace, tape.ErrEarlyWarning)
	options.Complete = func(context.Context) error { return nil }
	session, err := newTarTestWriteSession(device, options)
	require.NoError(t, err)
	result, err := session.Finalize(context.Background(), options.Stop)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Zero(t, device.queries)
	require.Zero(t, device.writes)
	require.Zero(t, device.marks)
}

func TestTarWritePositionsPublishOnlySettledWarning(t *testing.T) {
	for _, zero := range []bool{false, true} {
		for _, warning := range []bool{false, true} {
			t.Run(map[bool]string{false: "accepted", true: "unissued"}[zero]+map[bool]string{false: " cancel", true: " EW"}[warning], func(t *testing.T) {
				// The first member fits an accepted record; the later command can settle with zero or full acceptance.
				device := newTarWritePositionDevice()
				device.writeErrorAt = 3
				device.rejectWrite = zero
				device.writeErr = context.Canceled
				if warning {
					device.writeErrorAt = 0
					device.warningAt = 3
					if zero {
						device.warningAt = 0
						device.zeroWarningAt = 3
					}
				}
				options := tarTestOptions(t, false, 512)
				options.Navigator = device
				session, err := newTarTestWriteSession(device, options)
				require.NoError(t, err)
				writeTarPositionMember(t, session, "first", 1)
				ref, err := session.Target("later", 1024)
				require.NoError(t, err)
				_, cause := ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarWritePositionInfo{tarTestInfo{name: "later", size: 1024}}})
				require.Error(t, cause)

				// Only capacity settlement may validate accepted members and query its final position.
				result, err := session.Finalize(context.Background(), cause)
				if !warning {
					require.Nil(t, result)
					require.ErrorIs(t, err, ErrFinalizeUnusable)
					require.ErrorIs(t, err, context.Canceled)
					require.Zero(t, device.marks)
					require.Equal(t, 1, device.queries)
					return
				}
				require.NoError(t, err)
				files := tarTestCompleted(t, session, result)
				require.Len(t, files, 1)
				require.Equal(t, &entity.TapeBytePosition{LogicalObject: 100}, files[0].Storage.Metadata.GetTar().Header)
				require.Equal(t, &entity.TapeBytePosition{LogicalObject: 102}, files[0].Storage.Metadata.GetTar().End)
				require.Equal(t, 2, device.queries)
			})
		}
	}
}

func TestTarWritePositionLazyEmptyFileAndUnknownOrdinal(t *testing.T) {
	// A normal empty content archive emits its trailer only after taking a safe start observation.
	device := newTarWritePositionDevice()
	device.start.FileKnown = false
	options := tarTestOptions(t, false, 4096)
	options.Navigator = device
	session, err := newTarTestWriteSession(device, options)
	require.NoError(t, err)
	require.Zero(t, device.queries)
	result, err := session.Finalize(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, tarTestCompleted(t, session, result))
	require.Equal(t, 2, device.queries)
	require.Equal(t, 1, device.writes)
	require.Equal(t, 1, device.marks)
}

func TestTarWritePositionRejectsUnsafeStartAndRecordOverflow(t *testing.T) {
	for name, change := range map[string]func(tape.Position) tape.Position{
		"partition": func(p tape.Position) tape.Position { p.Partition = 1; return p },
		"file":      func(p tape.Position) tape.Position { p.File = 2; return p },
		"beginning": func(p tape.Position) tape.Position { p.Beginning = true; return p },
	} {
		t.Run(name, func(t *testing.T) {
			// The established content Tape file must agree before a header reaches the drive.
			device := newTarWritePositionDevice()
			device.start = change(device.start)
			options := tarTestOptions(t, false, 512)
			options.Navigator = device
			session, err := newTarTestWriteSession(device, options)
			require.NoError(t, err)
			ref, err := session.Target("member", 1)
			require.NoError(t, err)
			_, err = ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarWritePositionInfo{tarTestInfo{name: "member", size: 1}}})
			require.Error(t, err)
			result, err := session.Finalize(context.Background(), nil)
			require.Nil(t, result)
			require.ErrorIs(t, err, ErrFinalizeUnusable)
			require.Zero(t, device.writes)
			require.Zero(t, device.marks)
		})
	}

	// Address exhaustion drops provisional anchors while the remaining writes complete sequentially.
	device := newTarWritePositionDevice()
	device.bits = 32
	device.start.Object = math.MaxUint32 - 2
	options := tarTestOptions(t, false, 512)
	options.Navigator = device
	session, err := newTarTestWriteSession(device, options)
	require.NoError(t, err)
	ref, err := session.Target("overflow", 1)
	require.NoError(t, err)
	writer, err := ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarWritePositionInfo{tarTestInfo{name: "overflow", size: 1}}})
	require.NoError(t, err)
	_, err = writer.Write([]byte{1})
	require.NoError(t, err)
	require.NoError(t, writer.Finish(nil))
	result, err := session.Finalize(context.Background(), nil)
	require.NoError(t, err)
	files := tarTestCompleted(t, session, result)
	require.Len(t, files, 1)
	require.Nil(t, files[0].Storage.Metadata.GetTar().Header)
	require.Nil(t, files[0].Storage.Metadata.GetTar().End)
	require.Equal(t, 1, device.queries)
	require.Equal(t, 4, device.writes)
	require.Equal(t, 1, device.marks)
}

func TestTarWriteUnknownReportedFileKeepsIndependentOrdinal(t *testing.T) {
	// BOT/filemark navigation owns the file ordinal even when the optional query omits it.
	device := newTarWritePositionDevice()
	device.start.FileKnown = false
	options := tarTestOptions(t, false, 4096)
	options.Navigator = device
	session, err := newTarTestWriteSession(device, options)
	require.NoError(t, err)
	writeTarPositionMember(t, session, "member", 1)
	result, err := session.Finalize(context.Background(), nil)
	require.NoError(t, err)
	files := tarTestCompleted(t, session, result)
	require.Len(t, files, 1)
	locator := files[0].Storage.Metadata.GetTar()
	require.EqualValues(t, 1, locator.TapeFile)
	require.Equal(t, &entity.TapeBytePosition{LogicalObject: 100}, locator.Header)
	require.Equal(t, &entity.TapeBytePosition{LogicalObject: 100, OffsetBytes: 1024}, locator.End)
}

func TestTarWriteUnknownPositionIgnoresUndefinedAddress(t *testing.T) {
	for _, query := range []int{1, 2} {
		for _, fileKnown := range []bool{false, true} {
			t.Run(fmt.Sprintf("query%d/file-known=%t", query, fileKnown), func(t *testing.T) {
				// LONU leaves both partition and object undefined; independently exact file facts still apply.
				device := newTarWritePositionDevice()
				device.observe = func(call int, p tape.Position) (tape.Position, error) {
					if call == query {
						p.ObjectKnown = false
						p.Object = math.MaxUint64
						p.Partition = math.MaxUint32
						p.FileKnown = fileKnown
					}
					return p, nil
				}
				options := tarTestOptions(t, false, 4096)
				options.Navigator = device
				session, err := newTarTestWriteSession(device, options)
				require.NoError(t, err)
				writeTarPositionMember(t, session, "sequential", 1)
				result, err := session.Finalize(context.Background(), nil)
				require.NoError(t, err)
				files := tarTestCompleted(t, session, result)
				require.Len(t, files, 1)
				locator := files[0].Storage.Metadata.GetTar()
				require.Nil(t, locator.Header)
				require.Nil(t, locator.End)
				require.EqualValues(t, 1, locator.TapeFile)
			})
		}
	}
}

func TestTarWritePositionUnsupportedCommandLeaves(t *testing.T) {
	for _, asc := range []byte{0x20, 0x24} {
		for _, query := range []int{1, 2} {
			t.Run(fmt.Sprintf("ASC=%02x/query%d", asc, query), func(t *testing.T) {
				// Raw READ POSITION preserves the typed clean command rejection instead of a sentinel.
				device := newTarWritePositionDevice()
				device.observe = func(call int, p tape.Position) (tape.Position, error) {
					if call == query {
						return p, fmt.Errorf("READ POSITION failed, %w", &tape.CommandError{Response: tape.Response{Completed: true, Status: 2}, Sense: tape.Sense{Key: 5, ASC: asc}})
					}
					return p, nil
				}
				options := tarTestOptions(t, false, 4096)
				options.Navigator = device
				session, err := newTarTestWriteSession(device, options)
				require.NoError(t, err)
				writeTarPositionMember(t, session, "sequential", 1)
				result, err := session.Finalize(context.Background(), nil)
				require.NoError(t, err)
				files := tarTestCompleted(t, session, result)
				require.Len(t, files, 1)
				require.Nil(t, files[0].Storage.Metadata.GetTar().Header)
				require.Nil(t, files[0].Storage.Metadata.GetTar().End)
			})
		}
	}
}

type tarPipelineWarningDevice struct {
	*tarTestDevice
	started chan struct{}
	release chan struct{}
}

func (d *tarPipelineWarningDevice) Write(ctx context.Context, data []byte) (tape.RecordResult, error) {
	if d.writes == 0 {
		close(d.started)
		<-d.release
	}
	return d.tarTestDevice.Write(ctx, data)
}

type tarPipelineItem struct {
	content acp.ContentSource
	source  string
	target  acp.ContentTarget
}

func (i *tarPipelineItem) Source() string                      { return i.source }
func (*tarPipelineItem) Targets() []string                     { return []string{"member"} }
func (i *tarPipelineItem) ContentSource() acp.ContentSource    { return i.content }
func (i *tarPipelineItem) ContentTargets() []acp.ContentTarget { return []acp.ContentTarget{i.target} }

func TestTarWriteTargetResolutionCanOverlapACPEarlyWarning(t *testing.T) {
	// Archive resolves the next manifest page while ACP owns the ordered target write lane.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	device := &tarPipelineWarningDevice{tarTestDevice: &tarTestDevice{warningAt: 1}, started: make(chan struct{}), release: make(chan struct{})}
	options := tarTestOptions(t, false, 512)
	session, err := newTarTestWriteSession(device, options)
	require.NoError(t, err)
	ref, err := session.Target("member", 1)
	require.NoError(t, err)
	filename := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.WriteFile(filename, []byte("saved"), 0644))
	results := make(chan []acp.Result, 1)
	engine, err := acp.NewStream(ctx, func(batch []acp.Result) error { results <- batch; return nil },
		acp.WithHashPolicy(acp.HashRead), acp.SetToDevice(DeviceOptions(AccessSequential)...), acp.WithReadBuffer(1))
	require.NoError(t, err)
	require.NoError(t, engine.Submit(&tarPipelineItem{source: filename, target: ref.Content}))
	select {
	case <-device.started:
	case <-ctx.Done():
		t.Fatal("ACP did not start the record write")
	}

	// Releasing WRITE and describing later references share no synchronization of mutable session stop state.
	close(device.release)
	for index := 0; index < 1000; index++ {
		_, err := session.Target("later", 1)
		require.NoError(t, err)
	}
	closeErr := engine.Close()
	waitErr := engine.Wait()
	require.ErrorIs(t, errors.Join(closeErr, waitErr), ErrTargetNoSpace)
	var batch []acp.Result
	select {
	case batch = <-results:
	case <-ctx.Done():
		t.Fatal("ACP did not report the warning")
	}
	require.Len(t, batch, 1)
	require.Len(t, batch[0].Targets, 1)
	require.ErrorIs(t, batch[0].Targets[0].Err, ErrTargetNoSpace)
	result, err := session.Finalize(context.Background(), ErrTargetNoSpace)
	require.NoError(t, err)
	require.Empty(t, tarTestCompleted(t, session, result))
	require.Equal(t, 1, device.writes)
	require.Equal(t, 1, device.marks)
}

type tarMixedWarningDevice struct {
	*tarTestDevice
	cause error
}

func (d *tarMixedWarningDevice) Write(ctx context.Context, data []byte) (tape.RecordResult, error) {
	result, err := d.tarTestDevice.Write(ctx, data)
	if d.writes == 3 {
		result.EarlyWarning = true
		return result, errors.Join(err, tape.ErrEarlyWarning, d.cause)
	}
	return result, err
}

func TestTarWriteMixedWarningEvidenceCannotPublish(t *testing.T) {
	for _, cause := range []error{tape.ErrUncertain, tape.ErrMalformed, errors.New("hardware failure")} {
		t.Run(cause.Error(), func(t *testing.T) {
			// A permissive provider must not convert a warning joined with real failure into a usable prefix.
			device := &tarMixedWarningDevice{tarTestDevice: &tarTestDevice{}, cause: cause}
			options := tarTestOptions(t, false, 512)
			complete, cleanup := 0, 0
			options.Complete = func(context.Context) error { complete++; return nil }
			options.Cleanup = func(context.Context) error { cleanup++; return nil }
			session, err := newTarTestWriteSession(device, options)
			require.NoError(t, err)
			writeTarPositionMember(t, session, "first", 1)
			ref, err := session.Target("later", 1)
			require.NoError(t, err)
			_, err = ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarWritePositionInfo{tarTestInfo{name: "later", size: 1}}})
			require.ErrorIs(t, err, cause)
			result, err := session.Finalize(context.Background(), err)
			require.Nil(t, result)
			require.ErrorIs(t, err, ErrFinalizeUnusable)
			require.ErrorIs(t, err, cause)
			require.Zero(t, complete)
			require.Equal(t, 1, cleanup)
		})
	}
}

func TestTarWriteTypedWarningCannotRescueCancellation(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "variable", true: "fixed"}[fixed], func(t *testing.T) {
			// Actual raw EW includes its current SSC count evidence and can also carry settled caller cancellation.
			warning := &tape.CommandError{Response: tape.Response{Completed: true, Status: 2}, Sense: tape.Sense{Key: 0, EOM: true, ASCQ: 2, InformationValid: true}}
			device := &tarMixedWarningDevice{tarTestDevice: &tarTestDevice{}, cause: errors.Join(warning, context.Canceled)}
			options := tarTestOptions(t, fixed, 512)
			options.Mode.WarningCounts = true
			session, err := newTarTestWriteSession(device, options)
			require.NoError(t, err)
			writeTarPositionMember(t, session, "first", 1)
			ref, err := session.Target("later", 1)
			require.NoError(t, err)
			_, cause := ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarWritePositionInfo{tarTestInfo{name: "later", size: 1}}})
			require.ErrorIs(t, cause, context.Canceled)

			// Known acceptance is still not permission to issue a rescue filemark after cancellation.
			result, err := session.Finalize(context.Background(), cause)
			require.Nil(t, result)
			require.ErrorIs(t, err, ErrFinalizeUnusable)
			require.ErrorIs(t, err, context.Canceled)
			require.Equal(t, 3, device.writes)
			require.Zero(t, device.marks)
		})
	}
}

func TestTarWriteObservedEarlyWarningClosesAdmission(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(fmt.Sprintf("object-known=%t", known), func(t *testing.T) {
			// A safe-start READ POSITION warning is already write-stop evidence, even without an object address.
			device := newTarWritePositionDevice()
			device.start.BeyondEarlyWarning = true
			device.start.ObjectKnown = known
			options := tarTestOptions(t, false, 4096)
			options.Navigator = device
			session, err := newTarTestWriteSession(device, options)
			require.NoError(t, err)
			ref, err := session.Target("pending", 1)
			require.NoError(t, err)
			_, cause := ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarWritePositionInfo{tarTestInfo{name: "pending", size: 1}}})
			require.ErrorIs(t, cause, ErrTargetNoSpace)
			require.ErrorIs(t, cause, tape.ErrEarlyWarning)
			result, err := session.Finalize(context.Background(), cause)
			require.NoError(t, err)
			require.Empty(t, tarTestCompleted(t, session, result))
			require.Zero(t, device.writes)
			require.Equal(t, 1, device.marks)
		})
	}
}

func TestTarWriteFinalizeRetainsWarningWithoutPhysicalFailure(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		for _, trailerOnly := range []bool{false, true} {
			for _, zero := range []bool{false, true} {
				t.Run(fmt.Sprintf("fixed=%t/trailer-only=%t/zero=%t", fixed, trailerOnly, zero), func(t *testing.T) {
					// Finalization may encounter the first warning after ACP has finished the member.
					device := newTarWritePositionDevice()
					warningAt, size, priorWrites := 1, 1, 0
					if trailerOnly {
						warningAt, size, priorWrites = 2, 3584, 1
					}
					if zero {
						device.zeroWarningAt = warningAt
					} else {
						device.warningAt = warningAt
					}
					options := tarTestOptions(t, fixed, 4096)
					options.Navigator = device
					cleanups := 0
					options.Cleanup = func(context.Context) error { cleanups++; return nil }
					session, err := newTarTestWriteSession(device, options)
					require.NoError(t, err)
					writeTarPositionMember(t, session, "member", size)
					require.Equal(t, priorWrites, device.writes)

					// A usable physical result carries the stop separately from its completion error.
					result, err := session.Finalize(context.Background(), nil)
					require.NoError(t, err)
					require.NotNil(t, result)
					require.ErrorIs(t, result.Stop, ErrTargetNoSpace)
					require.ErrorIs(t, result.Stop, tape.ErrEarlyWarning)
					require.Equal(t, warningAt, device.writes)
					require.Equal(t, 1, device.marks)
					require.Equal(t, 1, cleanups)
					require.Equal(t, 2, device.queries)
					files := tarTestCompleted(t, session, result)
					if zero && !trailerOnly {
						require.Empty(t, files)
					} else {
						require.Len(t, files, 1)
						require.NotNil(t, files[0].Storage.Metadata.GetTar().Header)
						require.NotNil(t, files[0].Storage.Metadata.GetTar().End)
					}
					if !zero && !fixed {
						require.Less(t, len(device.records[warningAt-1].data), 4096)
					}
				})
			}
		}
	}
}

func TestTarWriteEmptyFinalizeRetainsObservedWarning(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(fmt.Sprintf("object-known=%t", known), func(t *testing.T) {
			// No source opened: only normal empty finalization observes the prepared content start.
			device := newTarWritePositionDevice()
			device.start.BeyondEarlyWarning = true
			device.start.ObjectKnown = known
			options := tarTestOptions(t, false, 4096)
			options.Navigator = device
			session, err := newTarTestWriteSession(device, options)
			require.NoError(t, err)
			require.Zero(t, device.queries)
			result, err := session.Finalize(context.Background(), nil)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.ErrorIs(t, result.Stop, ErrTargetNoSpace)
			require.ErrorIs(t, result.Stop, tape.ErrEarlyWarning)
			require.Empty(t, tarTestCompleted(t, session, result))
			require.Zero(t, device.writes)
			require.Equal(t, 1, device.marks)
			queries := 1
			if known {
				queries = 2
			}
			require.Equal(t, queries, device.queries)
		})
	}
}

func TestTarWriteFinalizeRejectsCancellationWithBufferedSuffix(t *testing.T) {
	for _, cancellation := range []error{context.Canceled, context.DeadlineExceeded} {
		for _, acceptedPrefix := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/accepted-prefix=%t", cancellation, acceptedPrefix), func(t *testing.T) {
				// Cancellation between finished members and finalization does not flush the buffered suffix.
				device := newTarWritePositionDevice()
				options := tarTestOptions(t, false, 4096)
				options.Navigator = device
				session, err := newTarTestWriteSession(device, options)
				require.NoError(t, err)
				if acceptedPrefix {
					writeTarPositionMember(t, session, "first", 3584)
				}
				writeTarPositionMember(t, session, "buffered", 1)
				writes := device.writes

				// Neither a complete earlier record nor a buffered member can survive aborted finalization.
				cause := fmt.Errorf("caller stopped: %w", cancellation)
				result, err := session.Finalize(context.Background(), cause)
				require.Nil(t, result)
				require.ErrorIs(t, err, ErrFinalizeUnusable)
				require.ErrorIs(t, err, cancellation)
				require.Equal(t, writes, device.writes)
				require.Zero(t, device.marks)
				require.Equal(t, 1, device.queries)
			})
		}
	}
}

func TestTarWriteTypedZeroWarningPreservesAcceptedPrefix(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "variable", true: "fixed"}[fixed], func(t *testing.T) {
			// Qualified SSC counts use bytes in variable mode and records in fixed mode.
			units := int64(512)
			if fixed {
				units = 1
			}
			warning := &tape.CommandError{Response: tape.Response{Completed: true, Status: 2, Residual: 512},
				Sense: tape.Sense{Key: 0, EOM: true, ASCQ: 2, InformationValid: true, Information: units}}
			device := &tarMixedWarningDevice{tarTestDevice: &tarTestDevice{writeErrorAt: 3, rejectWrite: true}, cause: warning}
			options := tarTestOptions(t, fixed, 512)
			options.Mode.WarningCounts = true
			session, err := newTarTestWriteSession(device, options)
			require.NoError(t, err)
			writeTarPositionMember(t, session, "first", 1)
			ref, err := session.Target("later", 1)
			require.NoError(t, err)
			_, cause := ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarWritePositionInfo{tarTestInfo{name: "later", size: 1}}})
			require.ErrorIs(t, cause, ErrTargetNoSpace)
			result, err := session.Finalize(context.Background(), cause)
			require.NoError(t, err)
			require.Len(t, tarTestCompleted(t, session, result), 1)
			require.Equal(t, 3, device.writes)
			require.Len(t, device.records, 3)
		})
	}
}

func TestTarWriteUnsupportedQueryFlagsCannotPublish(t *testing.T) {
	for name, change := range map[string]func(*tape.CommandError){
		"filemark":     func(e *tape.CommandError) { e.Sense.Filemark = true },
		"EOM":          func(e *tape.CommandError) { e.Sense.EOM = true },
		"ILI":          func(e *tape.CommandError) { e.Sense.ILI = true },
		"host failure": func(e *tape.CommandError) { e.Response.HostStatus = 1 },
	} {
		for _, query := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/query%d", name, query), func(t *testing.T) {
				// Unsupported opcode sense does not excuse contradictory boundary or transport evidence.
				device := newTarWritePositionDevice()
				device.observe = func(call int, p tape.Position) (tape.Position, error) {
					if call == query {
						failure := &tape.CommandError{Response: tape.Response{Completed: true, Status: 2}, Sense: tape.Sense{Key: 5, ASC: 0x20}}
						change(failure)
						return p, failure
					}
					return p, nil
				}
				options := tarTestOptions(t, false, 4096)
				options.Navigator = device
				session, err := newTarTestWriteSession(device, options)
				require.NoError(t, err)
				ref, err := session.Target("member", 1)
				require.NoError(t, err)
				writer, cause := ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarWritePositionInfo{tarTestInfo{name: "member", size: 1}}})
				if query == 1 {
					require.Error(t, cause)
				} else {
					require.NoError(t, cause)
					_, err = writer.Write([]byte{1})
					require.NoError(t, err)
					require.NoError(t, writer.Finish(nil))
				}
				result, err := session.Finalize(context.Background(), cause)
				require.Nil(t, result)
				require.ErrorIs(t, err, ErrFinalizeUnusable)
			})
		}
	}
}
