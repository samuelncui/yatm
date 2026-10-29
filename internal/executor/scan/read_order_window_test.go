package scan

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/readplan"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// This codec-neutral provider exercises actual ACP source boundaries, not physical measurement.
type orderedScanSession struct {
	media.ReadSession
	lock           sync.Mutex
	sources        map[string]*scanEndpointSource
	objects        map[string]uint64
	native         map[string]string
	opened         []string
	cursor         uint64
	observations   int
	closes         int
	available      bool
	noProvider     bool
	prepareErr     error
	currentErr     error
	readErr        error
	readPanic      error
	cancel         context.CancelFunc
	extraRanges    int
	rangeCalls     int
	rangeCancel    context.CancelFunc
	planningBudget time.Duration
	rangeSource    func(context.Context, func(readplan.RangeEntry) error) error
}

func (s *orderedScanSession) WalkReadRanges(ctx context.Context, yield func(readplan.RangeEntry) error) error {
	if s.rangeSource == nil {
		return errors.New("current Index is unavailable")
	}
	return s.rangeSource(ctx, yield)
}

func (*orderedScanSession) Capabilities() media.Capabilities {
	return media.Capabilities{Read: media.AccessSequential}
}
func (s *orderedScanSession) ReadOrder(context.Context) (media.ReadOrderProvider, string, error) {
	if s.prepareErr != nil {
		return nil, "", s.prepareErr
	}
	if s.noProvider {
		return nil, "no applicable read parameters", nil
	}
	return s, "", nil
}
func (s *orderedScanSession) Source(r media.ReadRequest) (media.SourceRef, error) {
	if path := s.native[r.Path]; path != "" {
		return media.SourceRef{Path: path}, nil
	}
	return media.SourceRef{Content: &orderedScanSource{session: s, path: r.Path}}, nil
}
func (s *orderedScanSession) ReadPlanningBudget() time.Duration { return s.planningBudget }
func (s *orderedScanSession) Ranges(r media.ReadRequest) ([]readplan.Range, bool) {
	s.rangeCalls++
	if s.rangeCancel != nil {
		s.rangeCancel()
	}
	object := s.objects[r.Path]
	ranges := []readplan.Range{{Start: &entity.TapeBytePosition{LogicalObject: object}, End: &entity.TapeBytePosition{LogicalObject: object + 1}}}
	if s.extraRanges > 0 {
		interval := ranges[0]
		ranges = make([]readplan.Range, s.extraRanges)
		for i := range ranges {
			ranges[i] = interval
		}
	}
	return ranges, s.available
}
func (s *orderedScanSession) Current(context.Context) (readplan.State, bool, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.observations++
	return readplan.State{Position: &entity.TapeBytePosition{LogicalObject: s.cursor}}, true, s.currentErr
}
func (*orderedScanSession) AccessCost(a, b *entity.TapeBytePosition) (time.Duration, bool) {
	distance := a.LogicalObject - b.LogicalObject
	if a.LogicalObject < b.LogicalObject {
		distance = b.LogicalObject - a.LogicalObject
	}
	return time.Duration(distance) * time.Millisecond, true
}

func (s *orderedScanSession) AccessScore(a, b *entity.TapeBytePosition) (float64, bool) {
	cost, known := s.AccessCost(a, b)
	return float64(cost), known
}
func (s *orderedScanSession) Evaluate(state readplan.State, interval readplan.Range) (readplan.Transition, bool) {
	cost, known := s.AccessCost(state.Position, interval.Start)
	return readplan.Transition{Cost: cost, Next: readplan.State{Position: interval.End}, Method: readplan.MethodSeek}, known
}

type orderedScanSource struct {
	session *orderedScanSession
	path    string
}

func (s *orderedScanSource) Open(ctx context.Context) (fs.File, error) {
	// Retain actual source entry even when cancellation rejects the Open.
	s.session.lock.Lock()
	s.session.opened = append(s.session.opened, s.path)
	s.session.lock.Unlock()
	if s.session.cancel != nil {
		s.session.cancel()
		return nil, ctx.Err()
	}

	// Preserve acquired files and their optional capabilities alongside an Open error.
	reader, err := s.session.sources[s.path].Open(ctx)
	if reader == nil {
		return nil, err
	}
	return &orderedScanReader{File: reader, source: s}, err
}

type orderedScanReader struct {
	fs.File
	source *orderedScanSource
}

func (r *orderedScanReader) Xattrs() (map[string][]byte, error) {
	if source, ok := r.File.(acp.XattrReader); ok {
		return source.Xattrs()
	}
	return nil, nil
}

func (r *orderedScanReader) Read(data []byte) (int, error) {
	if r.source.session.readPanic != nil {
		panic(r.source.session.readPanic)
	}
	if r.source.session.readErr != nil {
		return 0, r.source.session.readErr
	}
	return r.File.Read(data)
}
func (r *orderedScanReader) Close() error {
	// Expose a new cursor only after the underlying handle has actually closed.
	err := r.File.Close()
	s := r.source.session
	s.lock.Lock()
	defer s.lock.Unlock()
	s.cursor = s.objects[r.source.path] + 1
	s.closes++
	return err
}
func scanOrderFixture(t *testing.T, names ...string) (*runner, []*Entry, *orderedScanSession) {
	t.Helper()
	// Whole-selection preparation owns a disposable route under the same configured Work boundary as production.
	r := newProgressRunner(t)
	r.exe = executor.New(nil, nil, nil, executor.Paths{Work: t.TempDir()}, executor.Scripts{}, nil)
	session := &orderedScanSession{sources: map[string]*scanEndpointSource{}, objects: map[string]uint64{}, native: map[string]string{}, available: true}
	rows := make([]*Entry, 0, len(names))
	for i, name := range names {
		data := []byte(name)
		path := filepath.Join(t.TempDir(), "metadata")
		require.NoError(t, os.WriteFile(path, data, 0600))
		info, err := os.Stat(path)
		require.NoError(t, err)
		session.sources[name] = &scanEndpointSource{content: data, info: info}
		session.objects[name] = uint64(100 - i*10)
		hash := sha256.Sum256(data)
		row := &Entry{Path: name, NeedsHash: true, StorageOrder: []byte{1}, Storage: &entity.StoragePosition{Order: []byte{1}}, Expected: &entity.ExpectedFile{SizeBytes: int64(len(data)), Sha256: hash[:]}}
		require.NoError(t, r.db.Create(row).Error)
		rows = append(rows, row)
	}
	return r, rows, session
}
func startOrderedScan(t *testing.T, ctx context.Context, r *runner, session *orderedScanSession, batch int) (*contentStream, *acp.StreamCopyer) {
	t.Helper()
	config := &Config{Spec: &entity.ScanJobSpec{MediaId: 1, SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ, ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES}}
	stream, err := openTestContentStream(t, ctx, r, config, &Scope{}, session, flushSettings(batch, 100, 3600000))
	require.NoError(t, err)
	engine, err := acp.NewStream(ctx, stream.onResults, acp.WithHashPolicy(acp.HashReadRefresh), acp.SetFromDevice(media.DeviceOptions(media.AccessSequential)...), acp.WithResultBatch(100), acp.WithResultBuffer(100), acp.WithResultFlushInterval(time.Hour))
	require.NoError(t, err)
	return stream, engine
}

func runOrderedScan(t *testing.T, ctx context.Context, r *runner, session *orderedScanSession, batch int) (*contentStream, error) {
	t.Helper()
	// Execute the real feed/drain lifecycle before checking result persistence and temporary ownership.
	stream, engine := startOrderedScan(t, ctx, r, session, batch)
	runErr := stream.consume(ctx, engine)
	result := errors.Join(runErr, stream.writer.Close())
	requireScanReadSequenceCleanup(t, stream)
	return stream, result
}

func TestScanReadSequenceCrossesPagesWithoutFlushingResults(t *testing.T) {
	// The first row mismatches ordinary content; a successful physical Close still permits the next page.
	r, rows, session := scanOrderFixture(t, "a", "b", "c", "d", "e")
	rows[0].Expected.Sha256 = make([]byte, 32)
	require.NoError(t, r.db.Save(rows[0]).Error)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := runOrderedScan(t, ctx, r, session, 2)
	require.NoError(t, err)

	// Handoff pages consume one complete route and cannot wait for delayed result flushing to advance.
	require.Equal(t, []string{"e", "d", "c", "b", "a"}, session.opened)
	require.Equal(t, 1, session.observations)
	require.Equal(t, 5, session.closes)
	require.EqualValues(t, 5, stream.processed)
	var stored []Entry
	require.NoError(t, r.db.Order("path").Find(&stored).Error)
	require.Equal(t, entity.ScanFinding_SCAN_FINDING_MISMATCH, stored[0].Finding)
	for _, row := range stored[1:] {
		require.Equal(t, entity.ScanFinding_SCAN_FINDING_MATCH, row.Finding)
	}
}

func TestScanReadSequenceFillsMissingPositionsFromCurrentIndex(t *testing.T) {
	// Migrated known copies have paths and sizes but no stored extents; source bytes remain ordinary reads.
	r, rows, session := scanOrderFixture(t, "a", "b", "c", "d")
	session.available = false
	for _, row := range rows {
		require.NoError(t, r.db.Model(row).Update("size", 1).Error)
	}
	reads := 0
	session.rangeSource = func(ctx context.Context, yield func(readplan.RangeEntry) error) error {
		reads++
		for _, path := range []string{"d", "c", "b", "a"} {
			object := session.objects[path]
			if err := yield(readplan.RangeEntry{Path: path, Size: 1,
				Ranges: []readplan.Range{{Start: &entity.TapeBytePosition{LogicalObject: object},
					End: &entity.TapeBytePosition{LogicalObject: object + 1}}}}); err != nil {
				return err
			}
		}
		return ctx.Err()
	}

	// The actual feeder consumes the enriched execution index across pages without editing frozen positions.
	stream, err := runOrderedScan(t, t.Context(), r, session, 2)
	require.NoError(t, err)
	require.Equal(t, 1, reads)
	require.Equal(t, []string{"d", "c", "b", "a"}, session.opened)
	require.EqualValues(t, 4, stream.processed)
	var stored []Entry
	require.NoError(t, r.db.Find(&stored).Error)
	for _, row := range stored {
		require.Equal(t, entity.ScanFinding_SCAN_FINDING_MATCH, row.Finding)
		require.Nil(t, row.Storage.Metadata)
	}
}
func TestScanOrderedAllSkippedPageAndOptionalFallback(t *testing.T) {
	for _, kind := range []string{"removed page", "no model", "unavailable ranges", "range budget", "native"} {
		t.Run(kind, func(t *testing.T) {
			r, rows, session := scanOrderFixture(t, "a", "b", "c", "d")
			switch kind {
			case "removed page":
				for _, row := range rows[:2] {
					require.NoError(t, r.db.Model(row).Update("change", entity.ScanChange_SCAN_CHANGE_REMOVED).Error)
				}
			case "no model":
				session.noProvider = true
			case "unavailable ranges":
				session.available = false
			case "range budget":
				session.extraRanges = readplan.MaxRanges + 1
			case "native":
				path := filepath.Join(t.TempDir(), "native")
				require.NoError(t, os.WriteFile(path, []byte("a"), 0600))
				session.native["a"] = path
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := runOrderedScan(t, ctx, r, session, 2)
			require.NoError(t, err)
			expected := []string{"a", "b", "c", "d"}
			observations := 0
			if kind == "native" {
				expected = []string{"d", "c", "b"}
				observations = 1
			}
			if kind == "removed page" {
				expected = []string{"d", "c"}
				observations = 1
			}
			require.Equal(t, expected, session.opened)
			require.Equal(t, observations, session.observations)
		})
	}
}

func TestScanReadSequenceKeepsNativePaths(t *testing.T) {
	for _, prebound := range []bool{false, true} {
		for _, available := range []bool{false, true} {
			t.Run(fmt.Sprintf("prebound=%t/ranges=%t", prebound, available), func(t *testing.T) {
				// LTFS can provide geometry while ACP owns ordinary native reads, including inventory-bound paths.
				r, rows, session := scanOrderFixture(t, "a", "b", "c", "d")
				session.available = available
				root := t.TempDir()
				for _, row := range rows {
					path := filepath.Join(root, row.Path)
					require.NoError(t, os.WriteFile(path, []byte(row.Path), 0600))
					session.native[row.Path] = path
					if prebound {
						require.NoError(t, r.db.Model(row).Update("source_path", path).Error)
					}
				}

				// Observe the real feeder's submissions while the normal ACP pipeline verifies all native content.
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				stream, engine := startOrderedScan(t, ctx, r, session, 2)
				t.Cleanup(func() {
					if stream.sequence != nil {
						require.NoError(t, stream.sequence.Close())
					}
				})
				var submitted []string
				native := true
				runErr := executor.RunCopyPages(ctx, engine, func(ctx context.Context) ([]acp.Item, error) {
					items, err := stream.nextPage(ctx)
					for _, item := range items {
						submitted = append(submitted, item.Source())
						native = native && item.(*contentItem).ContentSource() == nil
					}
					return items, err
				})
				require.NoError(t, errors.Join(runErr, stream.writer.Close()))
				require.NoError(t, stream.sequence.Close())
				requireScanReadSequenceCleanup(t, stream)

				// The complete order spans pages; absent geometry keeps every member in LOGICAL order.
				expected := []string{"a", "b", "c", "d"}
				if available {
					expected = []string{"d", "c", "b", "a"}
					require.Equal(t, 1, session.observations)
				} else {
					require.Zero(t, session.observations)
				}
				for i, name := range expected {
					expected[i] = filepath.Join(root, name)
				}
				require.Equal(t, expected, submitted)
				require.True(t, native)
				require.Equal(t, len(rows), session.rangeCalls)
				require.False(t, stream.trackReads)
				require.EqualValues(t, len(rows), stream.processed)
				var stored []Entry
				require.NoError(t, r.db.Order("path").Find(&stored).Error)
				for _, row := range stored {
					require.Equal(t, entity.ScanFinding_SCAN_FINDING_MATCH, row.Finding)
				}
			})
		}
	}
}

func TestScanOrderedOpenFailureDoesNotExamineUnstartedRows(t *testing.T) {
	r, _, session := scanOrderFixture(t, "a", "b", "c")
	session.available = false
	session.sources["a"].openErr = os.ErrPermission
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := runOrderedScan(t, ctx, r, session, 2)
	require.ErrorIs(t, err, os.ErrPermission)
	require.Equal(t, []string{"a"}, session.opened)
	var stored []Entry
	require.NoError(t, r.db.Order("path").Find(&stored).Error)
	require.Equal(t, entity.ScanFinding_SCAN_FINDING_UNREADABLE, stored[0].Finding)
	for _, row := range stored[1:] {
		require.Equal(t, entity.ScanFinding_SCAN_FINDING_NOT_CHECKED, row.Finding)
		require.Zero(t, row.CheckedAtNS)
		require.True(t, row.NeedsHash)
	}
}
func TestScanOrderPreparationFailureDrainsEngine(t *testing.T) {
	for _, boundary := range []string{"provider", "cursor"} {
		t.Run(boundary, func(t *testing.T) {
			r, _, session := scanOrderFixture(t, "a", "b")
			failure := errors.New("unsafe " + boundary)
			if boundary == "provider" {
				session.prepareErr = failure
			} else {
				session.currentErr = failure
			}
			_, err := runOrderedScan(t, context.Background(), r, session, 2)
			require.ErrorIs(t, err, failure)
			require.Empty(t, session.opened)
		})
	}
}

// Malformed FileInfo accessors fail observation before any content or output is accepted.
type panicOrderInfo struct {
	os.FileInfo
	cause error
}

func (i panicOrderInfo) Size() int64 { panic(i.cause) }
func TestScanOrderedFatalEngineEscapesUnsettledWindow(t *testing.T) {
	// A fatal reader panic must release a window even when ACP suppresses individual results.
	r, _, session := scanOrderFixture(t, "a", "b", "c")
	session.available = false
	failure := errors.New("source Read panic")
	session.readPanic = failure
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, engine := startOrderedScan(t, ctx, r, session, 2)
	done := make(chan error, 1)
	go func() { done <- errors.Join(stream.consume(ctx, engine), stream.writer.Close()) }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, failure)
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatal("fatal engine left the source window waiting for suppressed terminal results")
	}

	// A fatal transfer cannot manufacture findings for any member in the abandoned window.
	var stored []Entry
	require.NoError(t, r.db.Find(&stored).Error)
	for _, row := range stored {
		require.Equal(t, entity.ScanFinding_SCAN_FINDING_NOT_CHECKED, row.Finding)
		require.Zero(t, row.CheckedAtNS)
		require.True(t, row.NeedsHash)
	}
}
func TestScanOrderedReadFailureAndCancellationStopFuturePlanning(t *testing.T) {
	for _, boundary := range []string{"read failure", "stat failure", "xattrs failure", "metadata panic", "open with file", "cancel"} {
		t.Run(boundary, func(t *testing.T) {
			// Inject one source failure into the first window of a multi-page attempt.
			r, _, session := scanOrderFixture(t, "a", "b", "c", "d")
			session.available = false
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := errors.New("physical read failed")
			switch boundary {
			case "cancel":
				session.cancel = cancel
				failure = context.Canceled
			case "stat failure":
				session.sources["a"].statErr = failure
			case "xattrs failure":
				session.sources["a"].attrsErr = failure
			case "metadata panic":
				session.sources["a"].info = panicOrderInfo{FileInfo: session.sources["a"].info, cause: failure}
			case "open with file":
				session.sources["a"].openErr = failure
				session.sources["a"].openFileOnError = true
			default:
				session.readErr = failure
			}

			// The existing drain settles acquired handles before the failed attempt returns.
			_, err := runOrderedScan(t, ctx, r, session, 2)
			require.ErrorIs(t, err, failure)
			require.Equal(t, []string{"a"}, session.opened)
			require.Zero(t, session.observations)
			if boundary != "cancel" {
				require.Equal(t, 1, session.closes, "the authoritative engine drain must close its acquired source")
			}

			// Unstarted rows cannot become published output or invented content findings.
			var rows []Entry
			require.NoError(t, r.db.Order("path").Find(&rows).Error)
			if boundary != "cancel" {
				require.Equal(t, entity.ScanFinding_SCAN_FINDING_UNREADABLE, rows[0].Finding)
			}
			for _, row := range rows[1:] {
				require.Equal(t, entity.ScanFinding_SCAN_FINDING_NOT_CHECKED, row.Finding)
				require.Zero(t, row.CheckedAtNS)
				require.True(t, row.NeedsHash)
			}
		})
	}
}
func TestScanReadSequenceSQLBoundLeavesLogicalPageLimitIntact(t *testing.T) {
	for _, logical := range []bool{false, true} {
		t.Run(map[bool]string{false: "eligible", true: "logical"}[logical], func(t *testing.T) {
			// Observe both fixed-size planning reads and handoff joins without tying either to selection size.
			r, _, session := scanOrderFixture(t, "a", "b")
			session.noProvider = logical
			var limits []int
			require.NoError(t, r.db.Callback().Query().Before("gorm:query").Register("test:window-limit", func(tx *gorm.DB) {
				if tx.Statement.Table == "entries" {
					if limit, ok := tx.Statement.Clauses["LIMIT"].Expression.(clause.Limit); ok && limit.Limit != nil {
						limits = append(limits, *limit.Limit)
					}
				}
			}))
			t.Cleanup(func() { _ = r.db.Callback().Query().Remove("test:window-limit") })
			_, err := runOrderedScan(t, context.Background(), r, session, 2048)
			require.NoError(t, err)
			require.NotEmpty(t, limits)

			// LOGICAL keeps its normal page size; complete planning uses bounded manifest pages and ID joins.
			if logical {
				for _, limit := range limits {
					require.Equal(t, 2048, limit)
				}
				return
			}
			require.Contains(t, limits, batchSize)
			require.Contains(t, limits, 2)
			for _, limit := range limits {
				require.LessOrEqual(t, limit, media.ReadOrderMaxItems)
			}
		})
	}
}

func requireScanReadSequenceCleanup(t *testing.T, stream *contentStream) {
	t.Helper()
	if stream.sequence != nil {
		_, err := stream.sequence.Next(context.Background(), 1)
		require.ErrorContains(t, err, "closed")
	}
	entries, err := os.ReadDir(resource.TemporaryRoot(stream.runner.exe.Paths().Work))
	if os.IsNotExist(err) {
		return
	}
	require.NoError(t, err)
	require.Empty(t, entries, "Scan cannot retain a disposable route after ACP drains")
}

func TestScanReadSequenceDoesNotDependOnHandoffBatch(t *testing.T) {
	for _, batch := range []int{1, 2, 5} {
		t.Run(fmt.Sprint(batch), func(t *testing.T) {
			// The globally nearest first member lies beyond the first handoff page.
			r, _, session := scanOrderFixture(t, "a", "b", "c", "d", "e")
			_, err := runOrderedScan(t, context.Background(), r, session, batch)
			require.NoError(t, err)
			require.Equal(t, []string{"e", "d", "c", "b", "a"}, session.opened)
			require.Equal(t, 1, session.observations)
			require.Equal(t, 5, session.rangeCalls, "handoff never re-enters geometry or the route model")
		})
	}
}

func TestScanReadSequenceBudgetFallbackRetainsOriginalManifest(t *testing.T) {
	// Expire before the temporary index is complete, without cancelling the content session.
	r, _, session := scanOrderFixture(t, "a", "b", "c", "d", "e")
	session.planningBudget = time.Nanosecond
	stream, err := runOrderedScan(t, context.Background(), r, session, 2)
	require.NoError(t, err)
	require.Nil(t, stream.sequence)
	require.Equal(t, []string{"a", "b", "c", "d", "e"}, session.opened)
	require.Zero(t, session.observations)
	require.EqualValues(t, 5, stream.processed)
}

func TestScanReadSequenceBudgetFallbackKeepsSourceFailureBoundary(t *testing.T) {
	// The original manifest remains protected by real source closure after optional planning expires.
	r, _, session := scanOrderFixture(t, "a", "b", "c", "d")
	session.planningBudget = time.Nanosecond
	failure := errors.New("physical read failed after planning budget")
	session.readErr = failure
	_, err := runOrderedScan(t, context.Background(), r, session, 2)
	require.ErrorIs(t, err, failure)
	require.Equal(t, []string{"a"}, session.opened)
	require.Equal(t, 1, session.closes)
	require.Zero(t, session.observations)
}

func TestScanReadSequenceIndexingCancellationIsFatal(t *testing.T) {
	// User cancellation during geometry indexing cannot turn into a successful LOGICAL fallback.
	r, _, session := scanOrderFixture(t, "a", "b", "c")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session.rangeCancel = cancel
	_, err := runOrderedScan(t, ctx, r, session, 2)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, session.opened)
	require.Zero(t, session.observations)
}

func TestScanReadSequenceIndexesOnlyPendingScope(t *testing.T) {
	// Removed rows and work already settled or owned by another Location never enter this Media route.
	r, rows, session := scanOrderFixture(t, "a", "b", "c", "d", "e")
	require.NoError(t, r.db.Model(rows[0]).Update("change", entity.ScanChange_SCAN_CHANGE_REMOVED).Error)
	require.NoError(t, r.db.Model(rows[1]).Update("needs_hash", false).Error)
	require.NoError(t, r.db.Model(rows[2]).Update("published", true).Error)
	require.NoError(t, r.db.Model(rows[3]).Update("location_id", 7).Error)
	_, err := runOrderedScan(t, context.Background(), r, session, 2)
	require.NoError(t, err)
	require.Equal(t, []string{"e"}, session.opened)
	require.Equal(t, 1, session.rangeCalls)
	require.Zero(t, session.observations, "one selected member needs no route optimization")
}

// The remaining planner methods give the codec-neutral fake a certified exit and one spatial group.
func (*orderedScanSession) CanonicalReadState(span readplan.Span) (readplan.State, bool) {
	return readplan.State{Position: span.Ranges[len(span.Ranges)-1].End}, true
}
func (*orderedScanSession) FixedFirstReadState(readplan.Span) (readplan.State, bool) {
	return readplan.State{}, false
}
func (*orderedScanSession) ReadIndex(position *entity.TapeBytePosition) (readplan.Index, bool) {
	return readplan.Index{Groups: [][]byte{{1}}, Position: binary.BigEndian.AppendUint64(nil, position.LogicalObject)}, true
}
func (*orderedScanSession) Finish(readplan.State) (time.Duration, bool)   { return 0, true }
func (*orderedScanSession) ReadOrderPlanned(entity.TapeReadOrder, string) {}
