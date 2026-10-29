package restore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/readplan"
)

func (a *jobRestoreRunner) loadCopyRows(
	ctx context.Context,
	mediaID int64,
	sequential bool,
	cursorOrder []byte,
	cursorPath string,
	cursorID int64,
	limit int,
) ([]*Copy, error) {
	// Select the next bounded page in the access order required by this Media.
	query := a.db.WithContext(ctx).
		Where("status = ? AND media_id = ?", entity.CopyStatus_COPY_STATUS_PENDING, mediaID)
	if cursorID != 0 {
		if sequential {
			query = query.Where(
				"storage_order > ? OR "+
					"(storage_order = ? AND media_path > ?) OR "+
					"(storage_order = ? AND media_path = ? AND id > ?)",
				cursorOrder, cursorOrder, cursorPath, cursorOrder, cursorPath, cursorID,
			)
		} else {
			query = query.Where("media_path > ? OR (media_path = ? AND id > ?)", cursorPath, cursorPath, cursorID)
		}
	}
	var copies []*Copy
	order := "media_path, id"
	if sequential {
		order = "storage_order, media_path, id"
	}
	if err := query.Order(order).Limit(limit).Find(&copies).Error; err != nil {
		return nil, fmt.Errorf(
			"query Restore copy page failed, media_id=%d storage_order=%x media_path=%q id=%d, %w",
			mediaID,
			cursorOrder,
			cursorPath,
			cursorID,
			err,
		)
	}
	return copies, nil
}

// copyJob is one caller-owned Restore item: the frozen candidate it transfers and the paths ACP
// must use. It is pure data, so the results callback recovers the candidate by asserting
// Result.Job back to this type.
type copyJob struct {
	source  string
	content acp.ContentSource
	target  string
	copy    *copyCandidate
	opened  atomic.Bool
}

func (j *copyJob) Source() string { return j.source }

func (j *copyJob) Targets() []string { return []string{j.target} }

func (j *copyJob) ContentSource() acp.ContentSource {
	if j.content == nil {
		return nil
	}
	return &restoreTrackedSource{job: j}
}

type restoreTrackedSource struct{ job *copyJob }

func (s *restoreTrackedSource) Open(ctx context.Context) (fs.File, error) {
	s.job.opened.Store(true)
	return s.job.content.Open(ctx)
}

func (j *copyJob) ContentTargets() []acp.ContentTarget { return nil }

// copyItems feeds a fixed sequential execution order or ordinary random-access manifest pages.
// Every result is persisted independently by the results callback.
type copyItems struct {
	runner  *jobRestoreRunner
	mediaID int64
	session media.ReadSession
	batch   int

	cursorOrder []byte
	cursorPath  string
	cursorID    int64
	failure     error
	prepared    bool
	exhausted   bool
	sequence    *readplan.Sequence
}

// nextPage returns the next bounded page of transferable candidates, or io.EOF once the
// manifest is exhausted.
func (s *copyItems) nextPage(ctx context.Context) ([]acp.Item, error) {
	// Prepare the complete selected-Media order before ACP can move the source.
	if !s.prepared {
		if s.session.Capabilities().Read == media.AccessSequential {
			if err := s.prepareSequence(ctx); err != nil {
				return nil, err
			}
		}
		s.prepared = true
	}

	// Resolve only enough ranks to fill this handoff; local skips never discard later ranks.
	items := make([]acp.Item, 0, min(s.batch, batchSize))
	for len(items) < s.batch {
		page, err := s.nextCandidates(ctx, s.batch-len(items))
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		for _, copy := range page {
			s.advance(copy)
			item, err := s.resolve(ctx, copy)
			if err != nil {
				// Local failures leave this candidate pending while independent files continue.
				if err := s.recordFailure(ctx, copy, err); err != nil {
					return nil, err
				}
				continue
			}
			if item == nil {
				continue
			}
			items = append(items, item)
			if len(items) == s.batch {
				break
			}
		}
	}
	if len(items) == 0 {
		return nil, io.EOF
	}
	return items, nil
}

func (s *copyItems) consume(ctx context.Context, engine *acp.StreamCopyer) (rerr error) {
	// Always release a partially consumed order after ACP has drained accepted work.
	defer func() {
		if s.sequence != nil {
			rerr = errors.Join(rerr, s.sequence.Close())
		}
	}()
	return errors.Join(executor.RunCopyPages(ctx, engine, s.nextPage), s.failure)
}

func (s *copyItems) prepareSequence(ctx context.Context) error {
	// Optional estimates choose a complete route before any source is entered.
	ordered, ok := s.session.(media.OrderedReadSession)
	var provider media.ReadOrderProvider
	reason := "LOGICAL: access-cost provider unavailable"
	if ok {
		var err error
		provider, reason, err = ordered.ReadOrder(ctx)
		if err != nil {
			return err
		}
	}

	// Ordinary LOGICAL reads already have an indexed manifest order and need no disposable copy.
	if provider == nil {
		s.runner.logger.WithContext(ctx).WithField("read_order", reason).
			WithField("planning_ns", int64(0)).Info("Restore read order prepared")
		return nil
	}

	// The attempt holds its disposable index under the shared Work temporary root.
	started := time.Now()
	sequence, err := readplan.Plan(ctx, s.runner.exe.Paths().Work, provider, s.readSpans(ctx, provider))
	elapsed := time.Since(started).Nanoseconds()
	order := entity.TapeReadOrder_TAPE_READ_ORDER_LOGICAL
	switch {
	case errors.Is(err, readplan.ErrBudget):
		reason = "LOGICAL: read planning budget expired"
	case err != nil:
		return err
	default:
		s.sequence, order, reason = sequence, sequence.Order(), sequence.Reason()
	}

	// Explain the selected strategy once without mixing planned ranks with physical observations.
	ordered.ReadOrderPlanned(order, reason)
	s.runner.logger.WithContext(ctx).WithField("read_order", reason).
		WithField("planning_ns", elapsed).Info("Restore read order prepared")
	return nil
}

func (s *copyItems) readSpans(ctx context.Context, provider media.ReadOrderProvider) iter.Seq2[readplan.Span, error] {
	return func(yield func(readplan.Span, error) bool) {
		// Read the frozen baseline through indexed keysets, independently of execution page size.
		var cursorOrder []byte
		var cursorPath string
		var cursorID int64
		for {
			page, err := s.runner.loadCopyRows(ctx, s.mediaID, true, cursorOrder, cursorPath, cursorID, batchSize)
			if err != nil {
				yield(readplan.Span{}, err)
				return
			}
			if len(page) == 0 {
				return
			}
			copies, err := s.runner.copyCandidates(ctx, page)
			if err != nil {
				yield(readplan.Span{}, err)
				return
			}
			for _, copy := range copies {
				// Reused outputs do not move the Tape. Settle them before modeling its route;
				// ordinary reads perform this same check when their next page is resolved.
				reused, err := s.reuseOutput(ctx, copy)
				if err != nil {
					if err := s.recordFailure(ctx, copy, err); err != nil {
						yield(readplan.Span{}, err)
						return
					}
					continue
				}
				if reused {
					continue
				}
				span := readplan.Span{ID: copy.ID, Path: copy.MediaPath, Size: copy.Size}
				if ranges, known := provider.Ranges(media.ReadRequest{Path: copy.MediaPath,
					Storage: &entity.StoragePosition{Order: copy.StorageOrder, Metadata: copy.StorageMetadata}}); known {
					span.Ranges = ranges
				}
				if !yield(span, nil) {
					return
				}
			}
			last := page[len(page)-1]
			cursorOrder, cursorPath, cursorID = last.StorageOrder, last.MediaPath, last.ID
		}
	}
}

func (s *copyItems) nextCandidates(ctx context.Context, limit int) ([]*copyCandidate, error) {
	// Bound SQL membership and frozen File joins independently of the ACP handoff size.
	limit = min(limit, batchSize)

	// Media without a cost plan retain their ordinary indexed manifest query.
	if s.sequence == nil {
		copies, err := s.runner.loadCopyRows(ctx, s.mediaID, s.session.Capabilities().Read == media.AccessSequential,
			s.cursorOrder, s.cursorPath, s.cursorID, limit)
		if err != nil {
			return nil, err
		}
		return s.runner.copyCandidates(ctx, copies)
	}

	// Consume fixed ranks even when a prior result made an indexed candidate no longer pending.
	for !s.exhausted {
		ids, err := s.sequence.Next(ctx, limit)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			s.exhausted = true
			return nil, s.sequence.Close()
		}
		var copies []*Copy
		if err := s.runner.db.WithContext(ctx).
			Where("media_id = ? AND status = ? AND id IN ?", s.mediaID, entity.CopyStatus_COPY_STATUS_PENDING, ids).
			Find(&copies).Error; err != nil {
			return nil, fmt.Errorf("query Restore execution page failed, %w", err)
		}
		if len(copies) == 0 {
			continue
		}

		// SQL membership has no order guarantee; restore the sequence before joining frozen File facts.
		byID := make(map[int64]*Copy, len(copies))
		for _, copy := range copies {
			byID[copy.ID] = copy
		}
		ordered := make([]*Copy, 0, len(copies))
		for _, id := range ids {
			if copy := byID[id]; copy != nil {
				ordered = append(ordered, copy)
			}
		}
		return s.runner.copyCandidates(ctx, ordered)
	}
	return nil, nil
}

func (s *copyItems) advance(copy *copyCandidate) {
	s.cursorOrder = copy.StorageOrder
	s.cursorPath = copy.MediaPath
	s.cursorID = copy.ID
}

// reuseOutput settles verified local content without moving the Tape. A complete plan calls
// it before assigning ranks; ordinary paging calls it during resolution. Each checks only once.
func (s *copyItems) reuseOutput(ctx context.Context, copy *copyCandidate) (bool, error) {
	// A ready output retains its owning Media finalization obligation.
	var output File
	if err := s.runner.db.WithContext(ctx).Where("item_id = ?", copy.ItemID).Limit(1).Find(&output).Error; err != nil {
		return false, err
	}
	if output.Ready {
		if output.ReadMediaID != s.mediaID {
			return false, fmt.Errorf("pending Restore output requires finalization of Media %d", output.ReadMediaID)
		}
		matched, _, err := s.runner.outputMatches(ctx, output.Path, output.ActualHash, output.ActualSize)
		if err != nil {
			return false, err
		}
		if !matched {
			return false, &restoreItemError{fmt.Errorf("pending Restore output changed: %q", output.Path)}
		}
		return true, s.runner.stageOutput(ctx, copy, output.ActualHash, output.ActualSize, output.Damaged)
	}

	// Reuse only verified native outputs without reading Media again.
	matched, exists, err := s.runner.outputMatches(ctx, copy.TargetPath, copy.Hash, copy.Size)
	if err != nil {
		return false, err
	}
	if matched {
		if output.ReadMediaID != 0 && output.ReadMediaID != s.mediaID {
			return false, fmt.Errorf("pending Restore output requires finalization of Media %d", output.ReadMediaID)
		}
		return true, s.runner.stageOutput(ctx, copy, copy.Hash, copy.Size, false)
	}
	if exists {
		return false, &restoreItemError{fmt.Errorf("reserved Restore output changed: %q", copy.TargetPath)}
	}
	return false, nil
}

// resolve binds one pending read without repeating output checks already done by complete planning.
func (s *copyItems) resolve(ctx context.Context, copy *copyCandidate) (acp.Item, error) {
	if s.sequence == nil {
		reused, err := s.reuseOutput(ctx, copy)
		if err != nil || reused {
			return nil, err
		}
	}

	// Bind the frozen on-Media path without using an endpoint identifier as a filesystem path.
	source, err := s.session.Source(media.ReadRequest{Path: copy.MediaPath, Storage: &entity.StoragePosition{Order: copy.StorageOrder, Metadata: copy.StorageMetadata}})
	if err == nil {
		err = source.Validate()
	}
	if err != nil {
		// A candidate this attempt cannot reach is observed and reported exactly as a
		// failed read is.
		if recordErr := s.runner.observeReadFailure(ctx, copy, err); recordErr != nil {
			return nil, errors.Join(err, recordErr)
		}
		return nil, &restoreItemError{fmt.Errorf("resolve Restore Media source failed, path=%q, %w", copy.MediaPath, err)}
	}
	// Reserve the output before ACP enters its transfer lifecycle.
	if err := s.runner.beginOutput(ctx, copy); err != nil {
		return nil, err
	}
	identifier := source.Path
	if source.Content != nil {
		identifier = copy.MediaPath
	}
	return &copyJob{source: identifier, content: source.Content, target: s.runner.restoreTarget(copy.TargetPath), copy: copy}, nil
}

func (s *copyItems) recordFailure(ctx context.Context, copy *copyCandidate, err error) error {
	// Planning and execution share item-local reporting; cancellation/device failures stop both.
	var local *restoreItemError
	if !errors.As(err, &local) {
		return err
	}
	if recordErr := s.runner.recordItemFailure(ctx, copy, err); recordErr != nil {
		return errors.Join(err, recordErr)
	}
	if s.failure == nil {
		s.failure = err
	}
	return nil
}

func (a *jobRestoreRunner) observeReadFailure(ctx context.Context, copy *copyCandidate, readErr error) error {
	// Only item-local, definite failures become health facts; interruption and device loss remain attempt errors.
	if errors.Is(readErr, media.ErrReadUnavailable) {
		return nil
	}
	health := entity.PositionHealth_POSITION_HEALTH_UNKNOWN
	switch {
	case errors.Is(readErr, os.ErrNotExist):
		health = entity.PositionHealth_POSITION_HEALTH_MISSING
	case errors.Is(readErr, os.ErrPermission):
		health = entity.PositionHealth_POSITION_HEALTH_UNREADABLE
	default:
		return nil
	}
	return a.db.WithContext(ctx).Model(&Copy{}).Where("id = ?", copy.ID).
		Updates(map[string]any{"health": health, "health_checked_at_ns": time.Now().UnixNano(), "health_published": false}).Error
}

// storeCompletion persists one finished item through the results callback, so a Media attempt
// has exactly one place that records transfer outcomes.
func (a *jobRestoreRunner) storeCompletion(
	ctx context.Context,
	job *copyJob,
	result acp.Result,
) error {
	if result.Err != nil {
		// ACP could not process the item or abandoned it during a graceful stop: the copy
		// keeps its PENDING status and the attempt reports why.
		if err := a.observeReadFailure(ctx, job.copy, result.Err); err != nil {
			return err
		}
		return itemFailure(job, result.Err)
	}
	damaged, err := a.completeCopy(ctx, job, result)
	if err != nil {
		return err
	}
	// The staged facts come from ACP's real transferred bytes, never from a reread target.
	if err := a.stageOutput(ctx, job.copy, result.SHA256, result.Size, damaged); err != nil {
		return err
	}
	// The per-file completion line stays where the item's outcome is known, so a stopped attempt
	// logs exactly the files whose results it recorded.
	a.logger.WithContext(ctx).Infof("restore file finished, source=%q size=%d", job.source, result.Size)
	return nil
}

// itemFailure describes an item ACP did not transfer, preserving the original error.
type restoreItemError struct{ error }

func (e *restoreItemError) Unwrap() error { return e.error }

func itemFailure(job *copyJob, err error) error {
	if err == nil {
		err = fmt.Errorf("ACP abandoned the Restore item, media_path=%q", job.copy.MediaPath)
	}
	return &restoreItemError{err}
}

func (a *jobRestoreRunner) recordItemFailure(ctx context.Context, copy *copyCandidate, err error) error {
	// Preserve a per-file explanation without resetting ready outputs or other candidates.
	if writeErr := a.db.WithContext(ctx).Model(&File{}).Where("item_id = ? AND completed = ?", copy.ItemID, false).
		Update("result_message", err.Error()).Error; writeErr != nil {
		return writeErr
	}
	a.logger.WithContext(ctx).WithError(err).Warnf("Restore file failed, media_path=%q", copy.MediaPath)
	return nil
}

// completeCopy validates one ACP completion against the frozen candidate. It reports
// whether the transferred bytes are damaged and are still eligible for Restore, and returns
// an error only when the completion carries no verified output for this candidate.
func (a *jobRestoreRunner) completeCopy(
	ctx context.Context,
	job *copyJob,
	result acp.Result,
) (bool, error) {
	copy := job.copy

	// The submitted Item owns its resolved source; endpoint identifiers are never filesystem paths.
	if job.source == "" || (job.content != nil && job.source != copy.MediaPath) {
		return false, itemFailure(job, fmt.Errorf("restore copy result source mismatch, media_path=%q", copy.MediaPath))
	}
	if len(result.Targets) != 1 {
		return false, itemFailure(job, fmt.Errorf("restore file did not finish, media_path=%q", copy.MediaPath))
	}
	target := &result.Targets[0]
	if target.Err != nil {
		// Target errors keep their identity, so an exhausted destination stays recognizable.
		return false, itemFailure(job, fmt.Errorf("restore target failed, media_path=%q, %w", copy.MediaPath, target.Err))
	}
	if filepath.Clean(target.Path) != a.restoreTarget(copy.TargetPath) {
		return false, itemFailure(job, fmt.Errorf("restore copy result path mismatch, media_path=%q", copy.MediaPath))
	}
	if len(result.SHA256) != 32 {
		return false, itemFailure(job, fmt.Errorf("Restore returned invalid content hash, media_path=%q", copy.MediaPath))
	}

	// Record the read outcome before deciding whether its bytes may be published.
	damaged := result.Size != copy.Size || !bytes.Equal(result.SHA256, copy.Hash)
	health := entity.PositionHealth_POSITION_HEALTH_HEALTHY
	if damaged {
		health = entity.PositionHealth_POSITION_HEALTH_DAMAGED
	}
	if err := a.db.WithContext(ctx).Model(&Copy{}).Where("id = ?", copy.ID).
		Updates(map[string]any{"health": health, "health_checked_at_ns": time.Now().UnixNano(), "health_published": false}).Error; err != nil {
		return false, err
	}
	if !damaged || a.allowDamaged {
		return damaged, nil
	}

	// Only a complete read can produce salvage. ACP owns interrupted-target cleanup, and a
	// rejected mismatch is removed so the same operation can retry it.
	mismatch := fmt.Errorf("restore file checksum mismatch, media_path=%q", copy.MediaPath)
	if removeErr := os.Remove(a.restoreTarget(copy.TargetPath)); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return false, itemFailure(job, errors.Join(mismatch, removeErr))
	}
	return false, itemFailure(job, mismatch)
}
