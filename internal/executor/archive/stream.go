package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/internal/executor"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"gorm.io/gorm"
)

// copySource pages the PENDING manifest and hands each page to ACP as caller-owned items.
// It stops handing over items once a copy target reports its capacity boundary.
type copySource struct {
	runner   *jobArchiveRunner
	session  mediapkg.WriteSession
	pageSize int

	cursor    string
	page      []*Item
	index     int
	prepared  []*copyItem
	locations map[int64]bool
	readModes map[int64]bool

	// stopped is set once the Media refused the next file, so no later item is handed over.
	stopped bool
}

// nextPage returns the next bounded page of prepared items, or io.EOF once the manifest is
// exhausted or the Media reported its capacity boundary.
func (s *copySource) nextPage(ctx context.Context) ([]acp.Item, error) {
	if s.stopped {
		return nil, io.EOF
	}
	for s.index == len(s.page) {
		if err := s.loadPage(ctx); err != nil {
			return nil, err
		}
	}

	batch, err := s.prepareItems(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]acp.Item, 0, len(batch))
	for _, item := range batch {
		items = append(items, item)
	}
	return items, nil
}

func (s *copySource) consume(ctx context.Context, engine *acp.StreamCopyer) error {
	return executor.RunCopyPages(ctx, engine, s.nextPage)
}

// loadPage reads the next short manifest page after the previous page is fully translated.
func (s *copySource) loadPage(ctx context.Context) error {
	var page []*Item
	if err := s.runner.db.WithContext(ctx).
		Where("status = ? AND target_path > ?", entity.CopyStatus_COPY_STATUS_PENDING, s.cursor).
		Order("target_path").Limit(s.pageSize).Find(&page).Error; err != nil {
		return fmt.Errorf("query archive copy page failed, cursor=%q, %w", s.cursor, err)
	}
	if len(page) == 0 {
		return io.EOF
	}
	s.page, s.index = page, 0
	return nil
}

// prepareItems resolves the source path and the Media target of the next bounded prefix.
// A source it cannot resolve aborts the batch, and a target that reports its capacity
// boundary ends the batch after the file that does not fit.
func (s *copySource) prepareItems(ctx context.Context) ([]*copyItem, error) {
	if s.prepared == nil {
		s.prepared = make([]*copyItem, 0, s.pageSize)
		s.locations = make(map[int64]bool)
		if s.readModes == nil {
			s.readModes = make(map[int64]bool)
		}
	}
	for s.index < len(s.page) {
		item := &copyItem{runner: s.runner, item: s.page[s.index]}
		s.index++
		s.cursor = item.item.TargetPath

		// Translate one manifest row without retaining it after the bounded page advances.
		if err := s.resolveSource(ctx, item); err != nil {
			return nil, err
		}
		target, err := s.session.TargetPath(item.item.TargetPath, item.item.Size)
		if err != nil {
			// The refused file still receives one terminal outcome, and it and the remaining
			// PENDING suffix stay PENDING for the next Media attempt.
			s.stopped = true
			if errors.Is(err, mediapkg.ErrCapacityBoundary) {
				item.preparedErr = fmt.Errorf(
					"archive file does not fit on Media, path=%q size=%d, %w",
					item.item.TargetPath, item.item.Size, mediapkg.ErrTargetNoSpace,
				)
			} else {
				item.preparedErr = fmt.Errorf(
					"resolve archive Media target failed, path=%q, %w", item.item.TargetPath, err,
				)
			}
			s.prepared = append(s.prepared, item)
			return s.prepared, nil
		}
		item.mediaTarget = target
		s.prepared = append(s.prepared, item)
	}

	// Record relocated originals once per bounded page before the next manifest read.
	if len(s.locations) != 0 {
		properties := make([]executor.JobProperty, 0, len(s.locations))
		for id := range s.locations {
			properties = append(properties, executor.JobProperty{Key: executor.JobPropertyLocation, Value: id})
		}
		if err := s.runner.exe.AddJobProperties(ctx, s.runner.job.ID, properties...); err != nil {
			return nil, err
		}
		s.locations = make(map[int64]bool)
	}
	batch := s.prepared
	s.prepared = nil
	return batch, nil
}

// resolveSource chooses the physical source path once before the item enters ACP.
func (s *copySource) resolveSource(ctx context.Context, copy *copyItem) error {
	item := copy.item
	if item.Data.Expected == nil {
		if err := s.runner.captureRawInput(ctx, item); err != nil {
			return err
		}
		if err := s.runner.db.WithContext(ctx).Model(item).Updates(map[string]any{
			"data": item.Data,
			"size": item.Size,
		}).Error; err != nil {
			return err
		}
	}
	if !item.Data.LibrarySelected {
		return nil
	}

	// Resolve the selected File's current original without re-admitting live roots.
	filename, locationID, err := s.runner.exe.ResolveOriginal(ctx, item.Data.Expected)
	if err != nil {
		return err
	}
	if locationID != item.Data.Expected.OriginalLocationId && !s.locations[locationID] {
		s.locations[locationID] = true
	}
	useMmap, ok := s.readModes[locationID]
	if !ok {
		location, err := s.runner.exe.Lib().GetLocation(ctx, locationID)
		if err != nil {
			return err
		}
		useMmap = location.Config.GetUseMmap()
		s.readModes[locationID] = useMmap
	}
	copy.useMmap = useMmap
	item.Data.SourcePath = filename
	if err := s.runner.db.WithContext(ctx).Model(&Item{}).
		Where("id = ? AND status = ?", item.ID, entity.CopyStatus_COPY_STATUS_PENDING).
		Update("data", item.Data).Error; err != nil {
		return err
	}
	return nil
}

// copyItem is the caller-owned ACP item. It keeps its own manifest row, the resolved target
// path, and the outcome preparation could not resolve, so the results callback recovers all of
// it from Result.Job alone.
type copyItem struct {
	runner  *jobArchiveRunner
	item    *Item
	useMmap bool

	// mediaTarget is the target ACP is asked to write; preparedErr holds a target outcome
	// preparation already decided, which the item reports without a copy.
	mediaTarget string
	preparedErr error
}

var _ acp.Item = (*copyItem)(nil)

func (c *copyItem) Source() string {
	return c.item.Data.GetSourcePath()
}

func (c *copyItem) ReadMode() acp.ReadMode {
	if c.useMmap {
		return acp.ReadMapped
	}
	return acp.ReadBuffered
}

func (c *copyItem) Targets() []string {
	if c.preparedErr != nil {
		return nil
	}
	return []string{c.mediaTarget}
}

// itemOutcome is one copy result waiting for the writer.
type itemOutcome struct {
	id         int64
	source     string
	mediaPath  string
	copyResult *entity.ArchiveCopyResult
}

// reporter is the result path of one copy attempt. The results callback validates what ACP
// reports, hands the outcomes a durable row can express to the writer, and returns; the shared
// writer stages each batch outside the feed. A result no durable row can express is kept as the attempt's error
// and the run continues, because its PENDING row is what a later attempt retries.
type reporter struct {
	runner *jobArchiveRunner
	// store persists one batch of outcomes; a failure of it stops the run.
	store  func(context.Context, []itemOutcome) error
	writer *executor.ResultWriter[itemOutcome]

	mutex    sync.Mutex
	flushed  int64
	failed   int64
	flushErr error
}

func newReporter(ctx context.Context, runner *jobArchiveRunner) (*reporter, error) {
	value := &reporter{runner: runner, store: runner.flushOutcomes}
	writer, err := executor.NewResultWriter(ctx, value.writeBatch)
	if err != nil {
		return nil, err
	}
	value.writer = writer
	return value, nil
}

// onResults validates one batch of results and hands the outcomes it can persist to the writer. A
// result that carries an error arrives on its own and is classified as it arrives; successes arrive
// in batches and keep the batch they arrived in. A staging failure is reported to ACP, which stops
// the feed exactly like a caller cancellation.
func (r *reporter) onResults(results []acp.Result) error {
	batch := make([]itemOutcome, 0, len(results))
	for _, result := range results {
		item, ok := result.Job.(*copyItem)
		if !ok {
			r.recordError(fmt.Errorf("archive copy result carries an unknown item"))
			continue
		}
		if result.Err != nil {
			// An item ACP could not process, or one a graceful stop abandoned, keeps its
			// PENDING row, because it produced no durable result this attempt.
			item.runner.handleItemFailure(item.item, result.Err, r)
			continue
		}
		outcome, err := item.runner.acceptCopyResult(item, result)
		if err != nil {
			r.recordError(err)
			item.runner.logUnwritten(item.item, err)
			continue
		}
		batch = append(batch, outcome)
	}
	return r.writer.Enqueue(batch...)
}

// writeBatch stages one batch of outcomes and counts what a durable row records.
func (r *reporter) writeBatch(ctx context.Context, batch []itemOutcome) error {
	if err := r.store(ctx, batch); err != nil {
		r.recordError(err)
		return err
	}
	r.mutex.Lock()
	r.flushed += int64(len(batch))
	r.mutex.Unlock()
	return nil
}

// Close drains the staged outcomes, waits for every write, and returns the terminal error.
func (r *reporter) Close() error {
	return r.writer.Close()
}

// err reports the first persistence failure or unpersistable result this attempt observed.
func (r *reporter) err() error {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return r.flushErr
}

// counters reports the persisted results and the items ACP could not process.
func (r *reporter) counters() (int64, int64) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return r.flushed, r.failed
}

func (r *reporter) addFailed() {
	r.mutex.Lock()
	r.failed++
	r.mutex.Unlock()
}

// recordError keeps the first per-item error that no durable result can express.
func (r *reporter) recordError(err error) {
	if err == nil {
		return
	}
	r.mutex.Lock()
	if r.flushErr == nil {
		r.flushErr = err
	}
	r.mutex.Unlock()
}

// flushOutcomes persists one batch of outcomes as one operation boundary, so a copy attempt
// performs a bounded number of database writes instead of one per item. An item with no
// durable result keeps its PENDING row and reports its own error through the runner.
func (a *jobArchiveRunner) flushOutcomes(ctx context.Context, batch []itemOutcome) error {
	staged := 0
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, outcome := range batch {
			if outcome.copyResult == nil || outcome.id == 0 {
				return fmt.Errorf("archive copy outcome is incomplete, id=%d source=%q", outcome.id, outcome.source)
			}
			updated := tx.Model(&Item{}).Where("id = ? AND status = ?", outcome.id, entity.CopyStatus_COPY_STATUS_PENDING).
				Updates(map[string]any{
					"status": entity.CopyStatus_COPY_STATUS_STAGED, "media_path": outcome.mediaPath, "result": outcome.copyResult,
				})
			if updated.Error != nil {
				return fmt.Errorf("stage archive item failed, id=%d, %w", outcome.id, updated.Error)
			}
			if updated.RowsAffected != 1 {
				return fmt.Errorf("stage archive item failed, id=%d affected=%d", outcome.id, updated.RowsAffected)
			}
			staged++
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("stage archive copy results failed, count=%d, %w", staged, err)
	}
	return nil
}

// acceptCopyResult records one successful ACP transfer result.
// It returns the row to persist, or the error that leaves the item PENDING.
func (a *jobArchiveRunner) acceptCopyResult(c *copyItem, result acp.Result) (itemOutcome, error) {
	item := c.item
	outcome := itemOutcome{id: item.ID, source: item.Data.GetSourcePath()}
	if len(result.Targets) == 0 {
		// An item whose target path could not be resolved reached its outcome without a copy.
		if c.preparedErr != nil {
			return outcome, c.preparedErr
		}
		return outcome, fmt.Errorf("archive file has no target result, source=%q", item.Data.SourcePath)
	}
	if len(result.Targets) != 1 {
		return outcome, fmt.Errorf("archive file has inconsistent target result, source=%q", item.Data.SourcePath)
	}
	target := result.Targets[0]
	if target.Err != nil {
		if errors.Is(target.Err, acp.ErrTargetNoSpace) {
			return outcome, fmt.Errorf(
				"archive Media has no space for file, path=%q, %w", item.TargetPath, mediapkg.ErrTargetNoSpace,
			)
		}
		return outcome, fmt.Errorf("archive file was not written, path=%q, %w", item.TargetPath, target.Err)
	}
	if target.Path != c.mediaTarget {
		return outcome, fmt.Errorf("archive copy result path mismatch, target=%q", target.Path)
	}
	if !result.Mode.IsRegular() {
		return outcome, fmt.Errorf("archive source is not an ordinary file, source=%q", item.Data.SourcePath)
	}
	if len(result.SHA256) != 32 {
		return outcome, fmt.Errorf("archive file has invalid SHA-256, source=%q", item.Data.SourcePath)
	}
	mtime, err := dataformat.Nanoseconds(result.ModTime)
	if err != nil {
		return outcome, err
	}
	writtenAt, err := dataformat.Nanoseconds(result.WriteTime)
	if err != nil {
		return outcome, err
	}
	copyResult := &entity.ArchiveCopyResult{
		SizeBytes:   result.Size,
		Mode:        uint32(result.Mode),
		ModTimeNs:   mtime,
		WriteTimeNs: writtenAt,
		Sha256:      append([]byte(nil), result.SHA256...),
	}
	if err := copyResult.Validate(); err != nil {
		return outcome, err
	}
	if err := entity.ValidateRelativePath(item.TargetPath); err != nil {
		return outcome, fmt.Errorf("invalid archive Media path, source=%q, %w", item.Data.SourcePath, err)
	}
	outcome.mediaPath = item.TargetPath
	outcome.copyResult = copyResult
	return outcome, nil
}

// handleItemFailure records an item ACP could not process at all. The item keeps its
// PENDING row, because it produced no durable result this attempt.
func (a *jobArchiveRunner) handleItemFailure(item *Item, err error, reporter *reporter) {
	reporter.addFailed()
	a.logUnwritten(item, err)
}

// logUnwritten reports one file this attempt did not write, classifying target failures through
// stable error identity for diagnostic logs.
func (a *jobArchiveRunner) logUnwritten(item *Item, err error) {
	if item == nil {
		return
	}
	source, size := item.Data.GetSourcePath(), item.Size
	switch {
	case errors.Is(err, acp.ErrTargetNoSpace), errors.Is(err, mediapkg.ErrTargetNoSpace),
		errors.Is(err, mediapkg.ErrCapacityBoundary):
		a.logger.WithField("reason", archiveMediaNoSpaceReason).Warnf(
			"archive file not written, source=%q size=%d", source, size,
		)
	case errors.Is(err, context.Canceled):
		// A graceful stop abandons the items it already accepted, which is not a finding.
		a.logger.Debugf("archive copy stopped before file, source=%q", source)
	default:
		a.logger.WithError(err).Warnf("archive file could not be processed, source=%q", source)
	}
}
