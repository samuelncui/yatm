package restore

import (
	"context"
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/readplan"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// This codec-neutral provider exercises Restore ordering and ACP settlement, not physical measurement.
type orderedRestoreSession struct {
	media.ReadSession
	lock           sync.Mutex
	sources        map[string]*restoreContentSource
	objects        map[string]uint64
	ends           map[string]uint64
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
	closePath      string
	closing        chan struct{}
	closeRelease   <-chan struct{}
	cancel         context.CancelFunc
	extraRanges    int
	planningBudget time.Duration
}

func (*orderedRestoreSession) Capabilities() media.Capabilities {
	return media.Capabilities{Read: media.AccessSequential}
}
func (s *orderedRestoreSession) ReadOrder(context.Context) (media.ReadOrderProvider, string, error) {
	if s.prepareErr != nil {
		return nil, "", s.prepareErr
	}
	if s.noProvider {
		return nil, "no applicable read parameters", nil
	}
	return s, "", nil
}
func (s *orderedRestoreSession) Source(r media.ReadRequest) (media.SourceRef, error) {
	if path := s.native[r.Path]; path != "" {
		return media.SourceRef{Path: path}, nil
	}
	return media.SourceRef{Content: &orderedRestoreSource{session: s, path: r.Path}}, nil
}
func (s *orderedRestoreSession) Ranges(r media.ReadRequest) ([]readplan.Range, bool) {
	object := s.objects[r.Path]
	end := s.ends[r.Path]
	if end == 0 {
		end = object + 1
	}
	ranges := []readplan.Range{{Start: &entity.TapeBytePosition{LogicalObject: object}, End: &entity.TapeBytePosition{LogicalObject: end}}}
	if s.extraRanges > 0 {
		interval := ranges[0]
		ranges = make([]readplan.Range, s.extraRanges)
		for i := range ranges {
			ranges[i] = interval
		}
	}
	return ranges, s.available
}
func (s *orderedRestoreSession) Current(context.Context) (readplan.State, bool, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.observations++
	return readplan.State{Position: &entity.TapeBytePosition{LogicalObject: s.cursor}}, true, s.currentErr
}
func (*orderedRestoreSession) AccessCost(a, b *entity.TapeBytePosition) (time.Duration, bool) {
	distance := a.LogicalObject - b.LogicalObject
	if a.LogicalObject < b.LogicalObject {
		distance = b.LogicalObject - a.LogicalObject
	}
	return time.Duration(distance) * time.Millisecond, true
}

func (s *orderedRestoreSession) AccessScore(a, b *entity.TapeBytePosition) (float64, bool) {
	cost, known := s.AccessCost(a, b)
	return float64(cost), known
}
func (s *orderedRestoreSession) Evaluate(state readplan.State, interval readplan.Range) (readplan.Transition, bool) {
	cost, known := s.AccessCost(state.Position, interval.Start)
	return readplan.Transition{Cost: cost, Next: readplan.State{Position: interval.End}, Method: readplan.MethodSeek}, known
}

type orderedRestoreSource struct {
	session *orderedRestoreSession
	path    string
}

func (s *orderedRestoreSource) Open(ctx context.Context) (fs.File, error) {
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
	return &orderedRestoreReader{File: reader, source: s}, err
}

type orderedRestoreReader struct {
	fs.File
	source *orderedRestoreSource
}

func (r *orderedRestoreReader) Xattrs() (map[string][]byte, error) {
	if source, ok := r.File.(acp.XattrReader); ok {
		return source.Xattrs()
	}
	return nil, nil
}

func (r *orderedRestoreReader) Read(data []byte) (int, error) {
	// Preserve separate fatal-panic and ordinary I/O failure boundaries.
	if r.source.session.readPanic != nil {
		panic(r.source.session.readPanic)
	}
	if r.source.session.readErr != nil {
		return 0, r.source.session.readErr
	}
	return r.File.Read(data)
}
func (r *orderedRestoreReader) Close() error {
	// Keep the actual Close observable while ACP still owns this source.
	err := r.File.Close()
	s := r.source.session
	if s.closePath == r.source.path {
		close(s.closing)
		<-s.closeRelease
	}

	// Expose the next cursor only after source settlement.
	s.lock.Lock()
	defer s.lock.Unlock()
	s.cursor = s.objects[r.source.path] + 1
	s.closes++
	return err
}
func restoreOrderFixture(t *testing.T, names ...string) (*jobRestoreRunner, []*copyCandidate, *orderedRestoreSession) {
	t.Helper()
	files := make(map[string][]byte, len(names))
	for _, name := range names {
		files[name] = []byte(name)
	}
	runner, _, copies := setupVolumeRestore(t, "ordered", files)
	session := &orderedRestoreSession{sources: map[string]*restoreContentSource{}, objects: map[string]uint64{}, native: map[string]string{}, available: true}
	for _, copy := range copies {
		// Equal physical order makes SQL path and ID the authoritative baseline tie breakers.
		require.NoError(t, runner.db.Model(&Copy{}).Where("id = ?", copy.ID).Updates(map[string]any{"storage_order": []byte{1}, "health": entity.PositionHealth_POSITION_HEALTH_UNKNOWN, "health_checked_at_ns": 0}).Error)
		path := filepath.Join(t.TempDir(), "metadata")
		require.NoError(t, os.WriteFile(path, files[copy.MediaPath], 0600))
		info, err := os.Stat(path)
		require.NoError(t, err)
		session.sources[copy.MediaPath] = &restoreContentSource{content: files[copy.MediaPath], info: info}
	}
	for i, name := range names {
		session.objects[name] = uint64(100 - i*10)
	}
	return runner, copies, session
}
func runOrderedRestore(t *testing.T, ctx context.Context, runner *jobRestoreRunner, copies []*copyCandidate, session *orderedRestoreSession, batch int) error {
	t.Helper()
	buffer := startTestBuffer(t, runner)
	items := &copyItems{runner: runner, mediaID: copies[0].MediaID, session: session, batch: batch}
	engine, err := acp.NewStream(ctx, buffer.onResults, acp.WithHashPolicy(acp.HashReadRefresh), acp.Overwrite(false), acp.SetFromDevice(media.DeviceOptions(media.AccessSequential)...), acp.WithResultBatch(100), acp.WithResultBuffer(100), acp.WithResultFlushInterval(time.Hour))
	require.NoError(t, err)
	return errors.Join(items.consume(ctx, engine), buffer.Close())
}
func TestRestoreReadSequenceCrossesPagesWithoutFlushingResults(t *testing.T) {
	// Freeze a complete selection whose best order crosses the submission page boundaries.
	runner, copies, session := restoreOrderFixture(t, "a", "b", "c", "d", "e")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// The result batch remains delayed until the ordinary ACP drain.
	require.NoError(t, runOrderedRestore(t, ctx, runner, copies, session, 2))
	require.Equal(t, []string{"e", "d", "c", "b", "a"}, session.opened)
	require.Equal(t, 1, session.observations, "the complete order observes the cursor before any source opens")
	require.Equal(t, 5, session.closes)

	// ACP results stage verified outputs without claiming Media finalization.
	var outputs []File
	require.NoError(t, runner.db.Find(&outputs).Error)
	require.Len(t, outputs, 5)
	for _, output := range outputs {
		require.True(t, output.Ready)
		require.False(t, output.Completed)
	}
}
func TestRestoreReadSequenceKeepsLocalSkipsWithoutReplanning(t *testing.T) {
	// Existing outputs can skip planned reads or fail locally without regenerating the order.
	runner, copies, session := restoreOrderFixture(t, "a", "b", "c", "d")
	for _, copy := range copies {
		if copy.MediaPath == "a" || copy.MediaPath == "b" {
			content := []byte(copy.MediaPath)
			if copy.MediaPath == "b" {
				content = []byte("changed")
			}
			path := runner.restoreTarget(copy.TargetPath)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
			require.NoError(t, os.WriteFile(path, content, 0600))
		}
	}

	// The fixed metadata plan stays intact while only the remaining sources enter ACP.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.ErrorContains(t, runOrderedRestore(t, ctx, runner, copies, session, 2), "reserved Restore output changed")
	require.Equal(t, []string{"d", "c"}, session.opened)
	require.Equal(t, 1, session.observations)
}
func TestRestoreReadSequenceFallbackAndNativeSources(t *testing.T) {
	for _, kind := range []string{"no model", "unavailable ranges", "range budget", "native"} {
		t.Run(kind, func(t *testing.T) {
			// Optional metadata gaps preserve the whole baseline; native paths remain eligible.
			runner, copies, session := restoreOrderFixture(t, "a", "b", "c", "d")
			switch kind {
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

			// One initial observation suffices for a complete optimized sequence.
			require.NoError(t, runOrderedRestore(t, context.Background(), runner, copies, session, 2))
			expected := []string{"a", "b", "c", "d"}
			observations := 0
			if kind == "native" {
				expected = []string{"d", "c", "b"}
				observations = 1
			}
			require.Equal(t, expected, session.opened)
			require.Equal(t, observations, session.observations)
		})
	}
}
func TestRestoreEndpointUnopenedCopiesKeepUnknownHealth(t *testing.T) {
	for _, logical := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordered", true: "no provider"}[logical], func(t *testing.T) {
			runner, copies, session := restoreOrderFixture(t, "a", "b", "c")
			session.available = false
			session.noProvider = logical
			session.sources["a"].openErr = os.ErrNotExist
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			require.ErrorIs(t, runOrderedRestore(t, ctx, runner, copies, session, 2), os.ErrNotExist)
			require.Equal(t, []string{"a"}, session.opened)
			var stored []Copy
			require.NoError(t, runner.db.Order("media_path").Find(&stored).Error)
			require.Equal(t, entity.PositionHealth_POSITION_HEALTH_MISSING, stored[0].Health)
			for _, copy := range stored[1:] {
				require.Equal(t, entity.PositionHealth_POSITION_HEALTH_UNKNOWN, copy.Health)
				require.Zero(t, copy.HealthCheckedAtNS)
				require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, copy.Status)
			}
		})
	}
}

func TestRestoreOrderPreparationFailureDrainsAlreadyRunningEngine(t *testing.T) {
	for _, boundary := range []string{"provider", "cursor"} {
		t.Run(boundary, func(t *testing.T) {
			runner, copies, session := restoreOrderFixture(t, "a", "b")
			failure := errors.New("unsafe " + boundary)
			if boundary == "provider" {
				session.prepareErr = failure
			} else {
				session.currentErr = failure
			}
			buffer := startTestBuffer(t, runner)
			items := &copyItems{runner: runner, mediaID: copies[0].MediaID, session: session, batch: 2}
			engine, err := acp.NewStream(context.Background(), buffer.onResults)
			require.NoError(t, err)
			require.ErrorIs(t, items.consume(context.Background(), engine), failure)
			require.NoError(t, engine.Wait())
			require.Error(t, engine.Submit(&acp.SimpleJob{Path: "after closed feed"}))
			require.Empty(t, session.opened)
		})
	}
}
func TestRestoreReadSequenceFailureAndCancellationStopLaterSources(t *testing.T) {
	for _, boundary := range []string{"read failure", "read panic", "stat failure", "xattrs failure", "open with file", "cancel"} {
		t.Run(boundary, func(t *testing.T) {
			// Planning finishes first; a failed source must still stop later physical reads.
			runner, copies, session := restoreOrderFixture(t, "a", "b", "c", "d")
			session.available = false
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := errors.New("physical read failed")
			switch boundary {
			case "cancel":
				session.cancel = cancel
				failure = context.Canceled
			case "read panic":
				session.readPanic = failure
			case "stat failure":
				session.sources["a"].statErr = failure
			case "xattrs failure":
				session.sources["a"].attrsErr = failure
			case "open with file":
				session.sources["a"].openErr = failure
				session.sources["a"].openFileOnError = true
			default:
				session.readErr = failure
			}

			// The existing drain settles acquired handles before the failed attempt returns.
			require.ErrorIs(t, runOrderedRestore(t, ctx, runner, copies, session, 2), failure)
			require.Equal(t, []string{"a"}, session.opened)
			require.Zero(t, session.observations)
			if boundary != "cancel" {
				require.Equal(t, 1, session.closes, "the authoritative engine drain must close its acquired source")
			}

			// Unstarted rows cannot become published output or invented content findings.
			var outputs []File
			require.NoError(t, runner.db.Find(&outputs).Error)
			for _, output := range outputs {
				require.False(t, output.Ready)
				require.False(t, output.Completed)
			}
		})
	}
}
func TestRestoreReadSequenceBoundsSQLReads(t *testing.T) {
	for _, logical := range []bool{false, true} {
		t.Run(map[bool]string{false: "eligible", true: "logical"}[logical], func(t *testing.T) {
			// Both planned and LOGICAL reads bound SQL work independently of the ACP handoff size.
			runner, copies, session := restoreOrderFixture(t, "a", "b")
			session.noProvider = logical
			var limits []int
			require.NoError(t, runner.db.Callback().Query().Before("gorm:query").Register("test:window-limit", func(tx *gorm.DB) {
				if tx.Statement.Table == "copies" {
					if limit, ok := tx.Statement.Clauses["LIMIT"].Expression.(clause.Limit); ok && limit.Limit != nil {
						limits = append(limits, *limit.Limit)
					}
				}
			}))
			t.Cleanup(func() { _ = runner.db.Callback().Query().Remove("test:window-limit") })

			// Optional planning does not change the bound on manifest reads.
			require.NoError(t, runOrderedRestore(t, context.Background(), runner, copies, session, 2048))
			require.NotEmpty(t, limits)
			for _, limit := range limits {
				require.Equal(t, batchSize, limit)
			}
		})
	}
}

// The remaining planner methods give the codec-neutral fake a certified exit and one spatial group.
func (*orderedRestoreSession) CanonicalReadState(span readplan.Span) (readplan.State, bool) {
	return readplan.State{Position: span.Ranges[len(span.Ranges)-1].End}, true
}
func (*orderedRestoreSession) FixedFirstReadState(readplan.Span) (readplan.State, bool) {
	return readplan.State{}, false
}
func (*orderedRestoreSession) ReadIndex(position *entity.TapeBytePosition) (readplan.Index, bool) {
	return readplan.Index{Groups: [][]byte{{1}}, Position: binary.BigEndian.AppendUint64(nil, position.LogicalObject)}, true
}
func (*orderedRestoreSession) Finish(readplan.State) (time.Duration, bool)   { return 0, true }
func (*orderedRestoreSession) ReadOrderPlanned(entity.TapeReadOrder, string) {}
