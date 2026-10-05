package scan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/internal/executor"
	mediapkg "github.com/samuelncui/yatm/internal/media"
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

	pageLimit int
	readModes map[int64]acp.ReadMode
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
}

func (i *contentItem) Source() string    { return i.SourcePath }
func (i *contentItem) Targets() []string { return nil }

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
	// a graceful stop left in flight, and the phase reports the writer's terminal error.
	writeErr := stream.writer.Close()
	if runErr == nil {
		runErr = writeErr
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
		return errors.Join(ctx.Err(), runErr)
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

func (s *contentStream) consume(ctx context.Context, engine *acp.StreamCopyer) error {
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
		var row *Entry
		switch item := result.Job.(type) {
		case *contentItem:
			row = item.Entry
		case *indexedContentItem:
			row = item.Entry
		default:
			continue
		}
		if accepted, ok := s.accept(row, result); ok {
			batch = append(batch, accepted)
		}
	}
	return s.writer.Enqueue(batch...)
}

// nextPage returns the next manifest page in deterministic physical read order. Paging preserves
// Tape locality without loading the whole manifest.
func (s *contentStream) nextPage(ctx context.Context) ([]acp.Item, error) {
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
			query := s.runner.scopeQuery(ctx, s.scope).Where("needs_hash = ? AND published = ?", true, false)
			order := "id"
			if s.session != nil && s.session.Capabilities().Read == mediapkg.AccessSequential {
				order = "storage_order, path, id"
				if s.cursor > 0 {
					query = query.Where("storage_order > ? OR (storage_order = ? AND path > ?) OR (storage_order = ? AND path = ? AND id > ?)", s.cursorOrder, s.cursorOrder, s.cursorPath, s.cursorOrder, s.cursorPath, s.cursor)
				}
			} else {
				query = query.Where("id > ?", s.cursor)
			}
			s.page, s.index = nil, 0
			if err := query.Order(order).Limit(s.pageLimit).Find(&s.page).Error; err != nil {
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
	return errors.Is(err, syscall.EIO) || errors.Is(err, syscall.ENODEV) || errors.Is(err, syscall.ENXIO)
}

func reusableSignature(filename string) (acp.CachedSignature, bool, error) {
	// Malformed disposable xattrs are misses; real access failures must not look like unsigned success.
	signature, valid, err := acp.ReadCachedSignature(filename)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) || isDeviceReadError(err) {
		return signature, false, err
	}
	return signature, valid && err == nil, nil
}
