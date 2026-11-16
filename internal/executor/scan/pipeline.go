package scan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	previewpkg "github.com/samuelncui/yatm/internal/preview"
	"github.com/samuelncui/yatm/internal/previewprotocol"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func (r *runner) runScope(ctx context.Context, config *Config, scope *Scope, target *entity.ReadMediaTarget) (returnErr error) {
	// Keep acquisition, pipeline work and physical cleanup inside the scope's observable duration.
	started := time.Now()
	fields := logrus.Fields{"location_id": scope.LocationID, "media_id": config.Spec.MediaId, "path": scope.Path}
	r.logInfo("Scan scope started", fields)
	defer func() { r.logResult("Scan scope finished", started, returnErr, fields) }()

	// Location manifests are complete; Media adapters acquire their physical session here.
	var local *locationStage
	var session mediapkg.ReadSession
	if scope.LocationID > 0 {
		var err error
		local, err = r.openLocation(ctx, scope)
		if err != nil {
			return err
		}
	} else if config.Spec.MediaId > 0 {
		var err error
		session, err = r.openSession(ctx, config, target)
		if err != nil {
			return err
		}
	}
	return r.runPipeline(ctx, config, scope, local, session)
}

func (r *runner) runPipeline(ctx context.Context, config *Config, scope *Scope, local *locationStage, session mediapkg.ReadSession) (returnErr error) {
	// A prepared source supplies enumeration and physical cleanup, not a separate processing flow.
	defer func() {
		if session != nil {
			returnErr = errors.Join(returnErr, session.Finalize(context.WithoutCancel(ctx), returnErr))
		}
	}()
	r.setPhase(entity.JobPhase_JOB_PHASE_INDEXING)
	if session != nil {
		if err := r.enumerateMedia(ctx, config, scope, session); err != nil {
			return err
		}
	}

	// Preview follows the rows whose content facts this attempt already saved, so it overlaps
	// content reading and comparison. It never stops them: its own failure fails the scope only
	// after they finish, while their failure or cancellation stops Preview before the scope returns.
	var settled *settledRows
	waitPreviews := func() error { return nil }
	if previewEnabled(config.Spec.PreviewPolicy) {
		settled = newSettledRows()
		previewCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- r.generatePreviews(previewCtx, config, scope, settled) }()
		var once sync.Once
		var previewErr error
		waitPreviews = func() error {
			once.Do(func() { previewErr = <-done; cancel() })
			return previewErr
		}
		defer func() { cancel(); _ = waitPreviews() }()
	}

	// Every source uses the same content policy, comparison and derivative sequence.
	readErr := r.readContent(ctx, config, scope, local, session, settled)
	settled.finish()
	checking := config.Spec.ResultPolicy == entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES
	if readErr != nil && !checking {
		return readErr
	}
	if config.Spec.CompareLibrary {
		r.setPhase(entity.JobPhase_JOB_PHASE_COMPARING_CONTENT)
		if err := r.compareContent(ctx, scope); err != nil {
			return errors.Join(readErr, err)
		}
	}
	if previewEnabled(config.Spec.PreviewPolicy) {
		// The phase reports Preview once it is the only remaining work, after every content fact
		// is known and its remaining generation workload can be classified.
		r.setPhase(entity.JobPhase_JOB_PHASE_GENERATING_PREVIEWS)
		previewErr := r.classifyPreviews(ctx, config, scope)
		previewErr = errors.Join(waitPreviews(), previewErr)
		if previewErr != nil {
			return errors.Join(readErr, previewErr)
		}
	}
	// Physical identity finalization gates publication of positive and negative observations alike.
	if session != nil {
		r.setPhase(entity.JobPhase_JOB_PHASE_FINALIZING_MEDIA)
		err := session.Finalize(context.WithoutCancel(ctx), readErr)
		session = nil
		if err != nil {
			return errors.Join(readErr, err)
		}
	}
	r.setPhase(entity.JobPhase_JOB_PHASE_PUBLISHING_SOURCE)
	if err := r.publishScope(context.WithoutCancel(ctx), config, scope, local); err != nil {
		return errors.Join(readErr, err)
	}
	return readErr
}

func (r *runner) enumerateMedia(ctx context.Context, config *Config, scope *Scope, session mediapkg.ReadSession) error {
	// Verification reads only its frozen baseline; missing physical paths become findings during the shared read stage.
	if config.Spec.ResultPolicy == entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES {
		if err := r.db.WithContext(ctx).Model(&Entry{}).Where("published = ?", false).Updates(map[string]any{
			"change": entity.ScanChange_SCAN_CHANGE_UNCHANGED, "needs_hash": true, "finding": entity.ScanFinding_SCAN_FINDING_NOT_CHECKED,
			"actual_size": 0, "actual_hash": nil, "checked_at_ns": 0, "detail": "", "compared": false,
			"preview_error": ""}).Error; err != nil {
			return err
		}
		// The work path is the only writer here, so one count replaces the bulk reset's results.
		if err := r.recountResults(ctx); err != nil {
			return err
		}
		// Indexing retains native paths; content endpoints are bound only while feeding ACP.
		return r.resolveMediaSources(ctx, session)
	}
	physical, ok := session.(mediapkg.InventorySession)
	if !ok {
		return fmt.Errorf("Media does not provide fresh inventory enumeration")
	}
	if err := physical.WalkInventory(ctx, func(file *mediapkg.InventoryEntry) error {
		facts, err := executor.InspectLocationFacts(file.Info)
		if err != nil {
			return err
		}
		var row Entry
		err = r.db.WithContext(ctx).Where("location_id = 0 AND path = ?", file.Path).First(&row).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		// The discovered counters replace this row's earlier contribution, if it had one.
		var found executor.Counts
		var results resultCounts
		if err == nil {
			results.add(&row, -1)
			if row.Change != entity.ScanChange_SCAN_CHANGE_REMOVED {
				found = executor.Counts{Files: -1, Bytes: -row.Size}
			}
		}
		row.Path = file.Path
		row.Size, row.Mode, row.MtimeNS = facts.SizeBytes, facts.Mode, facts.MtimeNs
		placementUnchanged := bytes.Equal(row.Storage.GetOrder(), file.Storage.GetOrder()) && proto.Equal(row.Storage.GetMetadata(), file.Storage.GetMetadata())
		row.Storage = file.Storage
		row.StorageOrder = append([]byte{}, file.Storage.GetOrder()...)
		row.Change = entity.ScanChange_SCAN_CHANGE_ADDED
		if row.PositionID > 0 {
			row.Change = entity.ScanChange_SCAN_CHANGE_CHANGED
		}
		source, err := session.Source(mediapkg.ReadRequest{Path: file.Path, Storage: file.Storage})
		if err == nil {
			err = source.Validate()
		}
		if err != nil {
			return err
		}
		row.SourcePath = source.Path
		row.NeedsHash = config.Spec.SignaturePolicy != entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY
		// A forced read changes evidence acquisition, not whether equal content is an inventory change.
		p := row.Expected
		metadataUnchanged := p != nil && p.SizeBytes == row.Size && p.Mode == row.Mode && p.MtimeNs == row.MtimeNS
		if metadataUnchanged && placementUnchanged {
			row.Change = entity.ScanChange_SCAN_CHANGE_UNCHANGED
		}
		if config.Spec.SignaturePolicy != entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ {
			if metadataUnchanged {
				row.SHA256, row.Signature = p.Sha256, p.Signature
				row.NeedsHash = len(row.SHA256) != 32 && row.NeedsHash
			}
			if source.Path != "" && session.Capabilities().Read != mediapkg.AccessSequential {
				cached, valid, err := reusableSignature(row.SourcePath)
				if err != nil {
					return err
				}
				if valid && cached.Size == row.Size && cached.MtimeNS == row.MtimeNS {
					setHash(&row, cached.SHA256[:])
				}
			}
		}
		if err := r.db.WithContext(ctx).Save(&row).Error; err != nil {
			return err
		}
		r.advanceCounts(executor.Counts{Files: found.Files + 1, Bytes: found.Bytes + row.Size}, executor.Counts{})
		results.add(&row, 1)
		r.advanceResults(results)
		return nil
	}); err != nil {
		return err
	}
	return nil
}

// resolveMediaSources stores the physical source path of every frozen verification entry for
// this attempt. A path the prepared Medium no longer offers is an independent per-file finding,
// because verification compares the frozen baseline against what is physically present; a device
// failure is not an observation of any remaining path.
func (r *runner) resolveMediaSources(ctx context.Context, session mediapkg.ReadSession) error {
	var after int64
	for {
		var rows []*Entry
		if err := r.db.WithContext(ctx).Where("needs_hash = ? AND published = ? AND change != ?", true, false, entity.ScanChange_SCAN_CHANGE_REMOVED).
			Where("id > ?", after).Order("id").Limit(batchSize).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		var results resultCounts
		for _, row := range rows {
			results.add(row, -1)
			after = row.ID
			source, err := session.Source(mediapkg.ReadRequest{Path: row.Path, Storage: row.Storage})
			if err == nil {
				err = source.Validate()
			}
			if err == nil {
				row.SourcePath = source.Path
				continue
			}
			if isDeviceReadError(err) {
				return err
			}
			r.logResult("Scan content file failed", time.Time{}, err, logrus.Fields{
				"path": row.Path, "location_id": row.LocationID,
			})
			recordReadFinding(row, err)
		}
		if err := r.saveEntries(ctx, rows); err != nil {
			return err
		}
		for _, row := range rows {
			results.add(row, 1)
		}
		r.advanceResults(results)
	}
}

func setHash(entry *Entry, hash []byte) {
	entry.SHA256 = append([]byte(nil), hash...)
	entry.NeedsHash = false
	entry.Signature, _ = library.NewFileSignature(hash, entry.Size)
	if before := entry.Before; before != nil && before.SizeBytes == entry.Size && bytes.Equal(before.Sha256, hash) && len(before.Signature) > 0 {
		entry.Signature = before.Signature
	}
	if expected := entry.Expected; expected != nil && expected.SizeBytes == entry.Size && bytes.Equal(expected.Sha256, hash) && len(expected.Signature) > 0 {
		entry.Signature = expected.Signature
	}
	if expected := entry.Expected; expected != nil && entry.Change == entity.ScanChange_SCAN_CHANGE_UNCHANGED && !bytes.Equal(expected.Sha256, hash) {
		entry.Change = entity.ScanChange_SCAN_CHANGE_CHANGED
	}
}

func (r *runner) scopeQuery(ctx context.Context, scope *Scope) *gorm.DB {
	return r.db.WithContext(ctx).Where("location_id = ?", scope.LocationID)
}

func (r *runner) eachEntry(ctx context.Context, scope *Scope, use func(*Entry) error) error {
	// All post-enumeration stages consume bounded manifest pages, never a filesystem-sized slice.
	var after int64
	for {
		var rows []*Entry
		if err := r.scopeQuery(ctx, scope).Where("id > ?", after).Order("id").Limit(batchSize).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			if err := use(row); err != nil {
				return err
			}
		}
		after = rows[len(rows)-1].ID
	}
}

func (r *runner) compareContent(ctx context.Context, scope *Scope) error {
	return r.eachEntry(ctx, scope, func(row *Entry) error {
		if row.Change == entity.ScanChange_SCAN_CHANGE_REMOVED || row.Compared {
			return nil
		}
		if len(row.Signature) > 0 {
			count, err := r.exe.Lib().CountSignatureCopies(ctx, row.Signature)
			if err != nil {
				return err
			}
			row.MatchingCopies = count
		}
		row.Compared = true
		if err := r.db.WithContext(ctx).Model(row).Updates(map[string]any{
			"matching_copies": row.MatchingCopies,
			"compared":        true,
		}).Error; err != nil {
			return err
		}
		r.advanceCounts(executor.Counts{}, executor.Counts{Files: 1, Bytes: row.Size})
		return nil
	})
}

// settledRows reports how far, in manifest ID order, this attempt has saved the content facts of
// the rows its content stage reads, so Preview can follow the read instead of waiting for all of
// it. Every row at or below the bound is either one the stage does not read or one whose result
// is saved. Only a stage that submits rows in ID order advances the bound; any other order leaves
// it until finish. A nil value is already finished.
type settledRows struct {
	lock    sync.Mutex
	changed chan struct{}
	pending []int64 // submitted IDs, ascending, front still unsaved
	saved   map[int64]bool
	last    int64
	bound   int64
	final   bool
}

func newSettledRows() *settledRows {
	return &settledRows{changed: make(chan struct{}), saved: make(map[int64]bool)}
}

// submit records rows handed to the read in ascending ID order.
func (s *settledRows) submit(ids []int64) {
	if s == nil || len(ids) == 0 {
		return
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	s.pending = append(s.pending, ids...)
	s.last = ids[len(ids)-1]
	s.advance()
}

// save records rows whose results the stage persisted.
func (s *settledRows) save(ids []int64) {
	if s == nil || len(ids) == 0 {
		return
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	for _, id := range ids {
		s.saved[id] = true
	}
	s.advance()
}

// advance moves the bound past the saved prefix of the submitted order; the caller holds the lock.
func (s *settledRows) advance() {
	for len(s.pending) > 0 && s.saved[s.pending[0]] {
		delete(s.saved, s.pending[0])
		s.pending = s.pending[1:]
	}
	bound := s.last
	if len(s.pending) > 0 {
		bound = s.pending[0] - 1
	}
	if bound > s.bound {
		s.bound = bound
		close(s.changed)
		s.changed = make(chan struct{})
	}
}

// finish releases every remaining row once the content stage has returned.
func (s *settledRows) finish() {
	if s == nil {
		return
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	if !s.final {
		s.final = true
		close(s.changed)
	}
}

// wait returns once the bound exceeds after or the stage finished.
func (s *settledRows) wait(ctx context.Context, after int64) (int64, bool, error) {
	for {
		s.lock.Lock()
		bound, final, changed := s.bound, s.final, s.changed
		s.lock.Unlock()
		if final || bound > after {
			return bound, final, nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return 0, false, ctx.Err()
		}
	}
}

// previewConcurrency bounds active Preview file callbacks. The Preview manager applies live
// capacity at each file slot; this traversal only sets a bounded upstream ceiling.
const previewConcurrency = 16

func (r *runner) eachPreviewEntry(ctx context.Context, scope *Scope, settled *settledRows, use func(context.Context, *Entry) error) error {
	// Keep at most one bounded manifest page and a fixed number of active file callbacks in memory.
	// The bound rolls over pages, so one slow file holds only its own slot.
	group, work := errgroup.WithContext(ctx)
	group.SetLimit(previewConcurrency)
	feed := func() error {
		var after int64
		for {
			bound, final, err := settled.wait(work, after)
			if err != nil {
				return err
			}
			query := r.scopeQuery(work, scope).Where("id > ?", after)
			if !final {
				query = query.Where("id <= ?", bound)
			}
			var rows []*Entry
			if err := query.Order("id").Limit(batchSize).Find(&rows).Error; err != nil {
				return err
			}
			if len(rows) == 0 {
				if final {
					return nil
				}
				after = bound
				continue
			}
			for _, row := range rows {
				if err := work.Err(); err != nil {
					return err
				}
				group.Go(func() error { return use(work, row) })
			}
			after = rows[len(rows)-1].ID
		}
	}
	feedErr := feed()
	if err := group.Wait(); err != nil {
		return err
	}
	if feedErr != nil {
		return feedErr
	}
	return ctx.Err()
}

// classifyPreviews classifies the scope's remaining unique generation workload once every content
// fact is known, and opens the Preview stage window. Generations this attempt already settled are
// part of its samples, not of the remaining workload.
func (r *runner) classifyPreviews(ctx context.Context, config *Config, scope *Scope) error {
	remaining, err := r.previewWorkload(ctx, config, scope)
	if err != nil {
		return err
	}
	r.lock.Lock()
	defer r.lock.Unlock()
	if !r.previewTiming.workKnown {
		r.previewTiming.workTotal, r.previewTiming.workKnown = r.previewTiming.samples+remaining, true
	}
	r.samplePreview()
	return nil
}

func (r *runner) generatePreviews(ctx context.Context, config *Config, scope *Scope, settled *settledRows) (returnErr error) {
	// Every exit clears the scope's timing, so a classification made meanwhile is never discarded.
	r.lock.Lock()
	r.contentScope = scope
	r.lock.Unlock()
	started := time.Now()
	var generated, reused, skipped, failed atomic.Int64
	defer func() {
		r.lock.Lock()
		// Outcomes accumulate over the attempt's scopes and outlive the phase, the way its speed
		// does; nothing about a successful Preview is stored per entry.
		r.previewOutcomes.ready += reused.Load() + generated.Load()
		r.previewOutcomes.skipped += skipped.Load()
		r.previewOutcomes.failed += failed.Load()
		r.previewTiming = previewTiming{}
		r.contentScope = nil
		r.lock.Unlock()
		r.logResult("Scan Preview scope finished", started, returnErr, logrus.Fields{
			"location_id": scope.LocationID, "path": scope.Path, "generated": generated.Load(),
			"reused": reused.Load(), "skipped": skipped.Load(), "failed": failed.Load(),
		})
	}()

	// Traverse bounded pages, allowing skips and reused assets to inform the observed workload mix.
	return r.eachPreviewEntry(ctx, scope, settled, func(ctx context.Context, row *Entry) error {
		// Only observed, supported content with usable facts can produce derivative assets.
		if row.Change == entity.ScanChange_SCAN_CHANGE_REMOVED {
			return nil
		}
		defer func() {
			r.lock.Lock()
			r.previewTiming.completed++
			r.samplePreview()
			r.lock.Unlock()
		}()
		// Nothing to do is not an outcome: a skipped entry stays empty and is reported as skipped.
		if !r.previewEligible(config, row) {
			skipped.Add(1)
			return nil
		}
		// The content store answers whether this content already has assets. Nothing about that
		// answer is stored per entry: the bundle's presence is the fact, and a Job keeps only the
		// files whose Preview could not be produced.
		signature, err := library.NewFileSignature(row.SHA256, row.Size)
		if err != nil {
			return r.recordPreviewFailure(ctx, row, err.Error())
		}
		if config.Spec.PreviewPolicy != entity.PreviewPolicy_PREVIEW_POLICY_REGENERATE_ALL {
			exists, err := r.exe.Previews().Exists(signature)
			if err != nil {
				return err
			}
			if exists {
				reused.Add(1)
				return r.clearPreviewFailure(ctx, row)
			}
		}
		// Assets are content-addressed, so duplicate entries share the same attempt result.
		generation, owner := r.beginPreviewGeneration(signature)
		if !owner {
			select {
			case <-generation.done:
			case <-ctx.Done():
				return ctx.Err()
			}
			if generation.err == nil {
				reused.Add(1)
				return r.clearPreviewFailure(ctx, row)
			}
			failed.Add(1)
			return r.recordPreviewFailure(ctx, row, generation.err.Error())
		}
		{
			// A missing bundle is generated. Reuse never reaches here, so no decoder time is spent
			// on content that already has assets.
			force := config.Spec.PreviewPolicy == entity.PreviewPolicy_PREVIEW_POLICY_REGENERATE_ALL

			// Bracket the actual manager call so metadata traversal never becomes decoder time.
			started := time.Now()
			fields := logrus.Fields{"path": row.Path, "location_id": row.LocationID}
			r.logInfo("Scan Preview started", fields)
			// Progress events stay counters rather than log lines; only the decoder start is a work sample.
			var actualGeneration atomic.Bool
			progressCtx := previewpkg.WithProgress(ctx, func(event previewprotocol.Event) {
				if event.Phase == "generate" {
					actualGeneration.Store(true)
				}
			})
			_, err := r.exe.Previews().Generate(
				progressCtx, row.SourcePath, row.SHA256, row.Size, row.MtimeNS, force, config.PreviewJobSettings,
			)
			r.finishPreviewGeneration(generation, err)
			if actualGeneration.Load() || err != nil {
				r.lock.Lock()
				if actualGeneration.Load() {
					r.previewTiming.samples++
				} else {
					// Pre-decoder failures have no remaining generation work and cannot provide a
					// rate sample; a disabled generator makes the whole workload unknown.
					r.previewTiming.workTotal = max(r.previewTiming.samples, r.previewTiming.workTotal-1)
				}
				if errors.Is(err, previewpkg.ErrDisabled) {
					// A runtime-disabled generator has no remaining workload to estimate.
					r.previewTiming.workTotal, r.previewTiming.workKnown = 0, true
				}
				r.lock.Unlock()
			}
			r.logResult("Scan Preview finished", started, err, fields)

			// Generator failure remains a per-item finding.
			if err != nil {
				failed.Add(1)
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return r.recordPreviewFailure(ctx, row, err.Error())
			}
			generated.Add(1)
		}
		return r.clearPreviewFailure(ctx, row)
	})
}

// recordPreviewFailure is the phase's only per-entry write: the bundle a Preview needs could not
// be produced, and this is why. Everything else about a Preview is the content store's answer.
// It writes only its own column, because comparison may update the same row concurrently.
func (r *runner) recordPreviewFailure(ctx context.Context, row *Entry, message string) error {
	var results resultCounts
	if row.PreviewError != "" {
		results.previewFailures--
	}
	row.PreviewError = message
	if err := r.db.WithContext(ctx).Model(row).Update("preview_error", message).Error; err != nil {
		return err
	}
	if message != "" {
		results.previewFailures++
	}
	r.advanceResults(results)
	return nil
}

// clearPreviewFailure drops a reason a previous attempt recorded for the same entry.
func (r *runner) clearPreviewFailure(ctx context.Context, row *Entry) error {
	if row.PreviewError == "" {
		return nil
	}
	return r.recordPreviewFailure(ctx, row, "")
}

func (r *runner) publishScope(ctx context.Context, config *Config, scope *Scope, local *locationStage) error {
	// Result policy changes only publication; all observations and content work already passed the shared stages.
	switch config.Spec.ResultPolicy {
	case entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS:
		if local == nil {
			return fmt.Errorf("original publication requires a Location")
		}
		if err := local.publish(ctx, scope); err != nil {
			return err
		}
	case entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_INVENTORY:
		if _, err := r.exe.Lib().ApplyScan(ctx, config.Spec.MediaId, func(ctx context.Context, yield func(*entity.ScanEntry) error) error {
			var after string
			for {
				var entries []*Entry
				if err := r.db.WithContext(ctx).Where("location_id = 0 AND path > ? AND change != ?", after, entity.ScanChange_SCAN_CHANGE_UNCHANGED).Order("path").Limit(batchSize).Find(&entries).Error; err != nil {
					return err
				}
				if len(entries) == 0 {
					return nil
				}
				for _, entry := range entries {
					if err := yield(entry.ToEntity()); err != nil {
						return err
					}
				}
				after = entries[len(entries)-1].Path
			}
		}); err != nil {
			return err
		}
	case entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES:
		if err := r.publishChecks(ctx, scope); err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) publishChecks(ctx context.Context, scope *Scope) error {
	return r.eachEntry(ctx, scope, func(row *Entry) error {
		if row.Published || row.Finding == entity.ScanFinding_SCAN_FINDING_NOT_CHECKED {
			return nil
		}
		health := entity.PositionHealth_POSITION_HEALTH_UNKNOWN
		switch row.Finding {
		case entity.ScanFinding_SCAN_FINDING_MATCH:
			health = entity.PositionHealth_POSITION_HEALTH_HEALTHY
		case entity.ScanFinding_SCAN_FINDING_MISMATCH:
			health = entity.PositionHealth_POSITION_HEALTH_DAMAGED
		case entity.ScanFinding_SCAN_FINDING_MISSING:
			health = entity.PositionHealth_POSITION_HEALTH_MISSING
		case entity.ScanFinding_SCAN_FINDING_UNREADABLE:
			health = entity.PositionHealth_POSITION_HEALTH_UNREADABLE
		}
		if health != entity.PositionHealth_POSITION_HEALTH_UNKNOWN {
			if err := r.exe.Lib().PublishPositionHealth(ctx, &library.PositionHealthObservation{PositionID: row.PositionID,
				Health: health, CheckedAtNS: row.CheckedAtNS, JobID: r.job.ID}); err != nil {
				return err
			}
		}
		row.Published = true
		return r.db.WithContext(ctx).Save(row).Error
	})
}
