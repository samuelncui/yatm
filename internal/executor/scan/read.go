package scan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hashicorp/go-multierror"
	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/internal/executor"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/readplan"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/sirupsen/logrus"
)

// contentStream owns the content phase's manifest paging and its shared result writer. It is the
// results callback of one ACP run: every outcome is validated against its own entry and handed to
// the writer, which persists the batch outside the read pipeline, so the callback never waits for
// a database write. ACP owns the result queue and the batch.
type contentStream struct {
	runner  *runner
	config  *Config
	scope   *Scope
	session mediapkg.ReadSession
	ctx     context.Context

	page        []*Entry
	index       int
	cursor      int64
	cursorOrder []byte
	cursorPath  string

	// writer persists the results the callback accepts. It is the phase's only persistence path,
	// and its terminal error is what the phase reports.
	writer *executor.ResultWriter[contentWrite]

	// lock guards the run counters.
	lock      sync.Mutex
	processed int64
	readBytes int64

	// totals is the attempt's durable denominator and completed base, read before ACP starts.
	totals contentTotals

	pageLimit    int
	readModes    map[int64]acp.ReadMode
	orderChecked bool
	provider     mediapkg.ReadOrderProvider
	sequence     *readplan.Sequence
	trackReads   bool
	reads        *mediapkg.ReadBatch
	hardStopped  <-chan struct{}
}

// contentWrite is one result waiting for the writer: a validated manifest row, or an item failure
// the callback could not accept as a content result.
type contentWrite struct {
	row   *Entry
	cause error
}

// contentItem is one manifest entry as an ACP item. It carries its own row, which is the state
// the results callback recovers by asserting Result.Job back to this type.
type contentItem struct {
	*Entry
	content acp.ContentSource
	opened  atomic.Bool
	reads   *mediapkg.ReadBatch
}

func (i *contentItem) Source() string {
	if i.content != nil {
		return i.Path
	}
	return i.SourcePath
}
func (i *contentItem) Targets() []string { return nil }

func (i *contentItem) ContentSource() acp.ContentSource {
	if i.content == nil {
		return nil
	}
	return &scanContentSource{item: i}
}

type scanContentSource struct{ item *contentItem }

func (s *scanContentSource) Open(ctx context.Context) (fs.File, error) {
	// Every endpoint policy retains actual source entry, independently of optional page planning.
	s.item.opened.Store(true)
	return s.item.content.Open(ctx)
}

func (i *contentItem) ContentTargets() []acp.ContentTarget { return nil }

// Indexed Preview input can contain originals from several Locations in one ACP stream.
type indexedContentItem struct {
	*contentItem
	mode acp.ReadMode
}

func (i *indexedContentItem) ReadMode() acp.ReadMode { return i.mode }

// newContentStream opens the content phase's manifest stream. The writer is created first, so an
// invalid execution setting fails before ACP can read anything.
func newContentStream(
	ctx context.Context, r *runner, config *Config, scope *Scope, session mediapkg.ReadSession, settings *entity.JobExecutionSettings,
) (*contentStream, error) {
	stream := &contentStream{
		runner: r, config: config, scope: scope, session: session, ctx: ctx,
		pageLimit: int(settings.GetReadBatch()), readModes: make(map[int64]acp.ReadMode),
	}
	// A write survives the operator's stop: a cancelled attempt still persists what it reported.
	// ACP owns the result queue and the batch, so the phase passes its write limits to the engine
	// in readContent instead of queueing results itself.
	writer, err := executor.NewResultWriter(context.WithoutCancel(ctx), stream.writeBatch)
	if err != nil {
		return nil, err
	}
	stream.writer = writer
	return stream, nil
}

func (r *runner) readContent(ctx context.Context, config *Config, scope *Scope, local *locationStage, session mediapkg.ReadSession) error {
	// Durable rows remain the authority for work earlier attempts completed. Read this attempt's
	// denominator and completed base once and publish them before the phase becomes observable, so
	// a request that sees the content phase always reads the attempt's snapshot instead of querying.
	totals, err := r.readContentTotals(ctx, scope)
	if err != nil {
		return err
	}
	r.inflightBytes.Store(0)
	defer r.inflightBytes.Store(0)

	// Reuse the limits frozen when this attempt was admitted.
	settings, err := executor.JobExecutionSettings(ctx)
	if err != nil {
		return err
	}
	// Start timing before ACP can read the first bytes or report its first progress event.
	stream, err := newContentStream(ctx, r, config, scope, session, settings)
	if err != nil {
		return err
	}
	// The writer drains accepted results on every return path, including a phase that returns
	// before it ever feeds ACP. Close is idempotent, so the run path below reads its error again.
	defer func() { _ = stream.writer.Close() }()
	stream.totals = totals
	r.publishContentStage(stream)

	// Reading is the only Scan phase that can expose ACP throughput.
	checking := config.Spec.ResultPolicy == entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES
	if checking {
		r.setPhase(entity.JobPhase_JOB_PHASE_VERIFYING_MEDIA)
	} else {
		r.setPhase(entity.JobPhase_JOB_PHASE_PROCESSING_CONTENT)
	}
	// The work path opens the stage window before ACP can report its first sample.
	r.sampleContentStage()

	// Cache-only misses remain unknown; they must never reach ACP's hashing fallback.
	if config.Spec.SignaturePolicy == entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY {
		return nil
	}
	options := []acp.Option{
		acp.WithHashPolicy(contentHashPolicy(config, session)),
		acp.WithReadBuffer(int(settings.GetReadBufferMax())),
		acp.WithLogger(r.logger),
		acp.WithEventHandler(stream.eventHandler()),
	}
	options = append(options, resultOptions(settings)...)
	if session != nil {
		options = append(options, acp.SetFromDevice(mediapkg.DeviceOptions(session.Capabilities().Read)...))
	} else if local != nil && local.source.Config.GetUseMmap() {
		options = append(options, acp.SetFromDevice(acp.WithReadMode(acp.ReadMapped)))
	}
	// An invalid stored setting is an option error, so it fails before the attempt opens a window.
	engine, err := acp.NewStream(ctx, stream.onResults, options...)
	if err != nil {
		return err
	}
	progress := r.getProgress()
	progress.StartSession()
	runErr := stream.consume(ctx, engine)

	// Drain before the progress checkpoint: every accepted result is persisted, including the ones
	// a graceful stop left in flight. A source error cannot hide a later persistence failure.
	writeErr := stream.writer.Close()
	if runErr == nil {
		runErr = writeErr
	} else if writeErr != nil && !errors.Is(runErr, writeErr) {
		runErr = multierror.Append(runErr, writeErr).ErrorOrNil()
	}
	readBytes, processed := stream.counters()
	progress.UpdateSessionCurrent(readBytes, processed)
	progress.CommitSession()
	r.inflightBytes.Store(0)
	if ctx.Err() != nil {
		// A stopped attempt reports the operator's cancellation even when the pipeline failed.
		if runErr == nil {
			return ctx.Err()
		}
		if errors.Is(runErr, ctx.Err()) {
			return runErr
		}
		return multierror.Append(ctx.Err(), runErr).ErrorOrNil()
	}
	return runErr
}

// resultOptions maps the operator's write limits onto ACP's result path. ACP owns the result queue
// and the result batch, so the shared writer only persists the batches the engine hands over.
func resultOptions(settings *entity.JobExecutionSettings) []acp.Option {
	return []acp.Option{
		acp.WithResultBuffer(int(settings.GetWriteBufferMax())),
		acp.WithResultBatch(int(settings.GetWriteBatchSize())),
		acp.WithResultFlushInterval(time.Duration(settings.GetFlushIntervalMs()) * time.Millisecond),
	}
}

// contentHashPolicy maps Scan's signature and result policies onto ACP's single content
// policy. KNOWN_ONLY never enters this phase; HashCachedOnly is the value that expresses it.
func contentHashPolicy(config *Config, session mediapkg.ReadSession) acp.HashPolicy {
	if config.Spec.SignaturePolicy == entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY {
		return acp.HashCachedOnly
	}
	if config.Spec.ResultPolicy == entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES {
		// Verification compares real content against frozen prior facts, never a stored hash.
		return acp.HashRead
	}
	if session == nil {
		// A Location observes its own content and refreshes the disposable cache.
		return acp.HashReadRefresh
	}
	if config.Spec.SignaturePolicy == entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ {
		// A forced read cannot reuse a stored hash, so it reads and refreshes the cache.
		return acp.HashReadRefresh
	}
	// Media use the cache like any other source, sequential Media included.
	return acp.HashCachedOrReadRefresh
}

// reuseAllowed reports whether a completed item may be satisfied by a stored hash instead of
// a real read. Only the fill-missing policy trusts the cache.
func (s *contentStream) reuseAllowed() bool {
	return s.config.Spec.SignaturePolicy == entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FILL_MISSING &&
		s.config.Spec.ResultPolicy != entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES
}

func (s *contentStream) consume(ctx context.Context, engine *acp.StreamCopyer) (rerr error) {
	// Release the disposable route only after ACP has drained every accepted source.
	defer func() {
		if s.sequence != nil {
			rerr = errors.Join(rerr, s.sequence.Close())
		}
	}()
	s.hardStopped = engine.HardStopped()
	return executor.RunCopyPages(ctx, engine, s.nextPage)
}

// onResults validates one batch of results and hands what it accepts to the writer. It performs no
// I/O and never waits for a database write: it only hands the results to the shared writer, and a
// full queue is the one place where it blocks. The recorded write error is what stops the feed,
// exactly like a caller cancellation: items already past the read stage still finish, and the
// items still inside the read pipeline are delivered as failures with this error.
func (s *contentStream) onResults(results []acp.Result) error {
	batch := make([]contentWrite, 0, len(results))
	for _, result := range results {
		var item *contentItem
		switch value := result.Job.(type) {
		case *contentItem:
			item = value
		case *indexedContentItem:
			item = value.contentItem
		default:
			continue
		}
		if item.reads != nil {
			if _, err := item.reads.AcknowledgeResult(item.ID); err != nil {
				return err
			}
		}
		if item.content != nil && !item.opened.Load() {
			// A copied stop cause is an attempt error; it cannot observe the unopened member's health.
			result.Err = errors.Join(mediapkg.ErrReadUnavailable, result.Err)
		}
		if accepted, ok := s.accept(item.Entry, result); ok {
			batch = append(batch, accepted)
		}
	}
	return s.writer.Enqueue(batch...)
}

// nextPage returns the next manifest page in deterministic physical read order. Paging preserves
// Tape locality without loading the whole manifest.
func (s *contentStream) nextPage(ctx context.Context) ([]acp.Item, error) {
	// Optional setup remains inside the feed owner, so even preparation failure drains ACP.
	if !s.orderChecked {
		s.orderChecked = true
		if err := s.prepareSequence(ctx); err != nil {
			return nil, err
		}
	}
	if s.sequence != nil || s.trackReads {
		return s.nextOrderedPage(ctx)
	}

	// Ordinary manifest reads keep their existing source binding and Indexed Preview behavior.
	items := make([]acp.Item, 0, s.pageLimit)
	for len(items) < s.pageLimit {
		row, err := s.nextRow(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		item := &contentItem{Entry: row}
		// Native paths stay frozen; endpoints bind the recorded path and storage at submission.
		if s.session != nil && row.SourcePath == "" {
			source, err := s.session.Source(mediapkg.ReadRequest{Path: row.Path, Storage: row.Storage})
			if err == nil {
				err = source.Validate()
			}
			if err != nil {
				return nil, err
			}
			item.SourcePath, item.content = source.Path, source.Content
		}
		if !s.config.IndexedInput {
			items = append(items, item)
			continue
		}
		mode, err := s.indexedReadMode(ctx, row.Expected.GetOriginalLocationId())
		if err != nil {
			return nil, err
		}
		items = append(items, &indexedContentItem{contentItem: item, mode: mode})
	}
	if len(items) == 0 {
		return nil, io.EOF
	}
	return items, nil
}

func (s *contentStream) indexedReadMode(ctx context.Context, locationID int64) (acp.ReadMode, error) {
	if locationID <= 0 {
		return acp.ReadBuffered, nil
	}
	if mode, ok := s.readModes[locationID]; ok {
		return mode, nil
	}
	location, err := s.runner.exe.Lib().GetLocation(ctx, locationID)
	if err != nil {
		return acp.ReadBuffered, err
	}
	mode := acp.ReadBuffered
	if location.Config.GetUseMmap() {
		mode = acp.ReadMapped
	}
	s.readModes[locationID] = mode
	return mode, nil
}

// nextRow returns the next manifest row in deterministic physical read order. Paging preserves
// Tape locality without loading the whole manifest.
func (s *contentStream) nextRow(ctx context.Context) (*Entry, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if s.index >= len(s.page) {
			var err error
			s.page, err = s.loadPage(ctx, s.pageLimit)
			s.index = 0
			if err != nil {
				return nil, err
			}
			if len(s.page) == 0 {
				return nil, io.EOF
			}
		}
		row := s.page[s.index]
		s.index++
		s.cursor = row.ID
		s.cursorOrder = row.StorageOrder
		s.cursorPath = row.Path
		if row.Change == entity.ScanChange_SCAN_CHANGE_REMOVED {
			continue
		}
		return row, nil
	}
}

// loadPage preserves the SQL baseline; callers freeze its high-water before any permutation.
func (s *contentStream) loadPage(ctx context.Context, limit int) ([]*Entry, error) {
	query := s.runner.scopeQuery(ctx, s.scope).Where("needs_hash = ? AND published = ?", true, false)
	order := "id"
	if s.session != nil && s.session.Capabilities().Read == mediapkg.AccessSequential {
		order = "storage_order, path, id"
		if s.cursor > 0 {
			query = query.Where("storage_order > ? OR (storage_order = ? AND path > ?) OR (storage_order = ? AND path = ? AND id > ?)",
				s.cursorOrder, s.cursorOrder, s.cursorPath, s.cursorOrder, s.cursorPath, s.cursor)
		}
	} else {
		query = query.Where("id > ?", s.cursor)
	}
	var page []*Entry
	if err := query.Order(order).Limit(limit).Find(&page).Error; err != nil {
		return nil, err
	}
	return page, nil
}

func (s *contentStream) prepareSequence(ctx context.Context) error {
	// The complete selected-Media route is frozen before the feed can open its first source.
	ordered, ok := s.session.(mediapkg.OrderedReadSession)
	if !ok || s.config.Spec.ResultPolicy != entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES {
		return nil
	}
	provider, reason, err := ordered.ReadOrder(ctx)
	if err != nil {
		return err
	}
	if provider == nil {
		return nil
	}

	// Independent input traversal leaves a complete manifest fallback available after a soft stop.
	s.provider, s.trackReads = provider, true
	started := time.Now()
	sequence, err := readplan.Plan(ctx, s.runner.exe.Paths().Work, provider, s.readSpans(ctx))
	elapsed := time.Since(started).Nanoseconds()
	order := entity.TapeReadOrder_TAPE_READ_ORDER_LOGICAL
	switch {
	case errors.Is(err, readplan.ErrBudget):
		s.provider = nil
		reason = "LOGICAL: read planning budget expired before the complete index was ready"
	case err != nil:
		return err
	default:
		s.sequence, order, reason = sequence, sequence.Order(), sequence.Reason()
	}
	ordered.ReadOrderPlanned(order, reason)

	// One attempt reports its strategy once; page size controls only the subsequent handoff.
	if s.runner.logger != nil {
		s.runner.logger.WithContext(ctx).WithField("read_order", reason).
			WithField("planning_ns", elapsed).Info("Scan read order prepared")
	}
	return nil
}

func (s *contentStream) readSpans(ctx context.Context) iter.Seq2[readplan.Span, error] {
	return func(yield func(readplan.Span, error) bool) {
		// A private keyset cursor leaves the original LOGICAL stream intact after a soft budget stop.
		cursor := &contentStream{runner: s.runner, scope: s.scope, session: s.session}
		for {
			page, err := cursor.loadPage(ctx, batchSize)
			if err != nil {
				yield(readplan.Span{}, err)
				return
			}
			if len(page) == 0 {
				return
			}
			last := page[len(page)-1]
			cursor.cursor, cursor.cursorOrder, cursor.cursorPath = last.ID, last.StorageOrder, last.Path
			for _, row := range page {
				if row.Change == entity.ScanChange_SCAN_CHANGE_REMOVED {
					continue
				}
				request := mediapkg.ReadRequest{Path: row.Path, Storage: row.Storage}
				if row.SourcePath != "" {
					s.trackReads = false
				} else {
					source, err := s.session.Source(request)
					if err == nil {
						err = source.Validate()
					}
					if err != nil {
						yield(readplan.Span{}, err)
						return
					}
					s.trackReads = s.trackReads && source.Content != nil
				}

				// Geometry belongs to Media, independently of native paths or endpoint-boundary tracking.
				ranges, available := s.provider.Ranges(request)
				if !available {
					ranges = nil
				}
				span := readplan.Span{ID: row.ID, Ranges: ranges, Path: row.Path, Size: row.Size}
				if !yield(span, nil) {
					return
				}
			}
		}
	}
}

func (s *contentStream) loadOrderedPage(ctx context.Context) ([]*Entry, error) {
	// A soft planning stop retains the original manifest and its source-boundary protection.
	if s.sequence == nil {
		page, err := s.loadPage(ctx, min(s.pageLimit, mediapkg.ReadOrderMaxItems))
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			return nil, io.EOF
		}
		last := page[len(page)-1]
		s.cursor, s.cursorOrder, s.cursorPath = last.ID, last.StorageOrder, last.Path
		selected := page[:0]
		for _, row := range page {
			if row.Change != entity.ScanChange_SCAN_CHANGE_REMOVED {
				selected = append(selected, row)
			}
		}
		return selected, nil
	}

	// The fixed route names existing manifest rows; only this handoff page needs hydration.
	ids, err := s.sequence.Next(ctx, min(s.pageLimit, mediapkg.ReadOrderMaxItems))
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, io.EOF
	}
	var page []*Entry
	if err := s.runner.scopeQuery(ctx, s.scope).
		Where("id IN ? AND needs_hash = ? AND published = ? AND change != ?", ids, true, false,
			entity.ScanChange_SCAN_CHANGE_REMOVED).Limit(len(ids)).Find(&page).Error; err != nil {
		return nil, fmt.Errorf("load Scan planned content page failed, %w", err)
	}
	rows := make(map[int64]*Entry, len(page))
	for _, row := range page {
		rows[row.ID] = row
	}
	selected := make([]*Entry, 0, len(page))
	for _, id := range ids {
		if row := rows[id]; row != nil {
			selected = append(selected, row)
		}
	}
	return selected, nil
}

func (s *contentStream) nextOrderedPage(ctx context.Context) ([]acp.Item, error) {
	for {
		// Source boundaries still stop the feed on failure, without repricing the fixed route.
		if s.reads != nil {
			if err := s.reads.WaitSources(ctx, s.hardStopped); err != nil {
				return nil, err
			}
		}
		page, err := s.loadOrderedPage(ctx)
		if err != nil {
			return nil, err
		}

		// Bind only the bounded handoff page; planning preflight never retained content endpoints.
		items := make([]*contentItem, 0, len(page))
		endpoints := s.trackReads
		for _, row := range page {
			item := &contentItem{Entry: row}
			if row.SourcePath == "" {
				source, err := s.session.Source(mediapkg.ReadRequest{Path: row.Path, Storage: row.Storage})
				if err == nil {
					err = source.Validate()
				}
				if err != nil {
					return nil, err
				}
				item.SourcePath, item.content = source.Path, source.Content
			}
			endpoints = endpoints && item.content != nil
			items = append(items, item)
		}
		if len(items) == 0 {
			continue
		}

		// Native sources retain the ordinary pipeline; endpoint pages keep the existing fatal-stop boundary.
		s.reads = nil
		s.trackReads = endpoints
		if endpoints {
			selected := make([]int64, len(items))
			for i, item := range items {
				selected[i] = item.ID
			}
			s.reads, err = mediapkg.NewReadBatch(selected)
			if err != nil {
				return nil, err
			}
			for _, item := range items {
				item.reads = s.reads
				item.content, err = s.reads.WrapSource(item.ID, item.content)
				if err != nil {
					return nil, err
				}
			}
		}
		result := make([]acp.Item, len(items))
		for i, item := range items {
			result[i] = item
		}
		return result, nil
	}
}

// accept validates one ACP result, updates the in-memory entry and returns the result to persist.
// It performs no I/O beyond the in-memory validation and no database write, so a slow consumer can
// never stall the read pipeline. The second value is false for an item that has no result to
// persist, which is what a graceful stop abandons.
func (s *contentStream) accept(row *Entry, result acp.Result) (contentWrite, bool) {
	// An item ACP could not process, and an item a graceful stop abandoned, arrive as failures.
	if result.Err != nil {
		return s.failed(row, result.Err)
	}
	// All content consumers use ACP's actual successful read, never target rereads or hand-written hash loops.
	if result.SignatureCacheHit && !s.reuseAllowed() {
		return s.failed(row, fmt.Errorf("Scan ACP result does not identify an actual selected read"))
	}
	if len(result.Targets) > 0 {
		return s.failed(row, fmt.Errorf("Scan ACP result reports unexpected copy targets"))
	}
	if len(result.SHA256) != 32 {
		return s.failed(row, fmt.Errorf("Scan ACP result has no SHA-256"))
	}
	mtime, err := dataformat.Nanoseconds(result.ModTime)
	if err != nil {
		return s.failed(row, err)
	}
	// Expected-copy comparison retains the old baseline even when a complete read returns different bytes.
	if s.config.Spec.ResultPolicy == entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES {
		row.Finding = entity.ScanFinding_SCAN_FINDING_UNVERIFIABLE
		if expected := row.Expected; expected != nil && len(expected.Sha256) == 32 && expected.SizeBytes >= 0 {
			row.Finding = entity.ScanFinding_SCAN_FINDING_MATCH
			if expected.SizeBytes != result.Size || !bytes.Equal(expected.Sha256, result.SHA256) {
				row.Finding = entity.ScanFinding_SCAN_FINDING_MISMATCH
			}
		}
		row.ActualSize, row.ActualHash, row.CheckedAtNS = result.Size, result.SHA256, time.Now().UnixNano()
		row.ReadMode, row.ReadMtimeNS = uint32(result.Mode), mtime
		row.NeedsHash = false
	} else {
		row.Size, row.Mode, row.MtimeNS = result.Size, uint32(result.Mode), mtime
		setHash(row, result.SHA256)
	}

	s.lock.Lock()
	s.processed++
	s.readBytes += result.Size
	s.lock.Unlock()
	return contentWrite{row: row}, true
}

// failed classifies one item ACP could not process at all. Items a graceful stop abandons arrive
// through the same callback; they are counted without inventing a per-file finding and without a
// result to persist.
func (s *contentStream) failed(row *Entry, cause error) (contentWrite, bool) {
	if cause == nil {
		cause = fmt.Errorf("Scan content item failed")
	}
	if errors.Is(cause, context.Canceled) || (s.ctx != nil && s.ctx.Err() != nil) {
		s.lock.Lock()
		s.processed++
		s.lock.Unlock()
		return contentWrite{}, false
	}
	return contentWrite{row: row, cause: cause}, true
}

// recordReadError classifies one file failure. Verification persists a per-file finding and
// continues; every other mode stops its scope by returning the cause.
func (s *contentStream) recordReadError(ctx context.Context, row *Entry, cause error) error {
	// Every file failure reaches the Job log, including failures discovered before ACP opens it.
	s.runner.logResult("Scan content file failed", time.Time{}, cause, logrus.Fields{
		"path": row.Path, "location_id": row.LocationID,
	})

	// Verification persists per-file findings while ordinary content failures stop their scope.
	if s.config.Spec.ResultPolicy != entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES {
		return cause
	}
	if isDeviceReadError(cause) {
		return cause
	}
	row.Finding = entity.ScanFinding_SCAN_FINDING_UNREADABLE
	if errors.Is(cause, os.ErrNotExist) {
		row.Finding = entity.ScanFinding_SCAN_FINDING_MISSING
	}
	row.Detail = cause.Error()
	row.CheckedAtNS = time.Now().UnixNano()
	row.NeedsHash = false
	if err := s.runner.db.WithContext(ctx).Save(row).Error; err != nil {
		return err
	}
	s.lock.Lock()
	s.processed++
	s.lock.Unlock()
	return nil
}

// writeBatch persists accepted results using bounded SQL statements independently of ACP batches.
// The whole batch is persisted
// before the first classification or persistence failure is reported, so a failed run never loses
// the results it already reported.
func (s *contentStream) writeBatch(ctx context.Context, batch []contentWrite) error {
	rows := make([]*Entry, 0, len(batch))
	var failure error
	note := func(err error) {
		if err != nil && failure == nil {
			failure = err
		}
	}
	for _, item := range batch {
		// An item failure is a per-file finding under verification and a scope stop otherwise.
		if item.cause != nil {
			note(s.recordReadError(ctx, item.row, item.cause))
			continue
		}
		rows = append(rows, item.row)
	}
	if len(rows) == 0 {
		return failure
	}
	if err := s.runner.saveEntries(ctx, rows); err != nil {
		if failure == nil {
			return err
		}
		return errors.Join(failure, err)
	}
	for _, row := range rows {
		s.logFinished(row)
	}
	return failure
}

func (s *contentStream) logFinished(row *Entry) {
	if row.Finding == entity.ScanFinding_SCAN_FINDING_UNREADABLE || row.Finding == entity.ScanFinding_SCAN_FINDING_MISSING {
		return
	}
	s.runner.logInfo("Scan content file finished", logrus.Fields{
		"path": row.Path, "location_id": row.LocationID, "bytes": row.Size, "finding": row.Finding.String()})
}

func (s *contentStream) counters() (int64, int64) {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.readBytes, s.processed
}

// eventHandler keeps runtime progress observable for a single large file without publishing
// catalog revisions. Item failures arrive through the item callbacks instead.
func (s *contentStream) eventHandler() acp.EventHandler {
	return func(event acp.Event) {
		value, ok := event.(*acp.EventUpdateProgress)
		if !ok {
			return
		}
		s.lock.Lock()
		processed, completedBytes := s.processed, s.readBytes
		s.lock.Unlock()
		s.runner.getProgress().UpdateSessionCurrent(value.Bytes, processed)
		inflight := value.Bytes - completedBytes
		if inflight < 0 {
			inflight = 0
		}
		s.runner.inflightBytes.Store(inflight)

		// The event that advances the work counters also samples the stage meter, so the estimate
		// no longer depends on a client polling and a hidden card cannot freeze it.
		s.runner.sampleContentStage()
	}
}

func isDeviceReadError(err error) bool {
	return errors.Is(err, syscall.EIO) || errors.Is(err, syscall.ENODEV) || errors.Is(err, syscall.ENXIO) ||
		errors.Is(err, tape.ErrUncertain) || errors.Is(err, tape.ErrOwnershipUnknown) || errors.Is(err, tape.ErrBusy) ||
		errors.Is(err, mediapkg.ErrReadUnavailable)
}

func reusableSignature(filename string) (acp.CachedSignature, bool, error) {
	// Malformed disposable xattrs are misses; real access failures must not look like unsigned success.
	signature, valid, err := acp.ReadCachedSignature(filename)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) || isDeviceReadError(err) {
		return signature, false, err
	}
	return signature, valid && err == nil, nil
}
