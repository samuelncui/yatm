package fileops

import (
	"context"
	"errors"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"gorm.io/gorm"
)

// ErrKeepNotMember reports a Keep request whose selected copy is not in the group.
var ErrKeepNotMember = errors.New("survivor is not a group member")

// keepMember stages validated physical identities without retaining a large group in memory.
type keepMember struct {
	FileID     int64                    `gorm:"primaryKey;autoIncrement:false"`
	LocationID int64                    `gorm:"index"`
	Reference  *entity.LocationEntryRef `gorm:"serializer:json"`
}

// KeepIdentical keeps one Location copy of an Identical group and removes the others
// through ordinary Remove batches scoped to the group's Locations.
func KeepIdentical(exe *executor.Executor, scope library.IdenticalScope, seed int64, req *entity.KeepIdenticalRequest, stream ResultStream) error {
	ids := make([]int64, 0, len(scope.Roots))
	for _, root := range scope.Roots {
		ids = append(ids, root.LocationID)
	}
	return WithLocationRemovals(exe, ids, func(removals *LocationRemovals) error {
		return keepAdmitted(exe, req, scope, seed, removals, stream)
	})
}

func keepAdmitted(exe *executor.Executor, req *entity.KeepIdenticalRequest, scope library.IdenticalScope, seed int64, removals *LocationRemovals, stream ResultStream) (rerr error) {
	// Validate the complete authoritative snapshot before any physical work starts.
	ctx := stream.Context()
	snapshot, err := exe.Lib().OpenIdenticalComponent(ctx, scope, seed)
	if err != nil {
		return err
	}
	defer func() { rerr = errors.Join(rerr, snapshot.Close()) }()
	group, err := snapshot.Group(req.GroupId)
	if err != nil {
		return library.ErrLocationConflict
	}
	if group.Fingerprint != req.Fingerprint {
		return library.ErrLocationConflict
	}
	stage, closeStage, err := openKeepStage(ctx, exe)
	if err != nil {
		return err
	}
	defer func() { rerr = errors.Join(rerr, closeStage()) }()
	summary := &entity.FileOperationSummary{TotalItemCount: group.Count - 1, UnprocessedCount: group.Count - 1, Dryrun: req.GetDryrun()}
	found := false
	err = snapshot.WalkMembers(group.ID, func(members []library.IdenticalMember) error {
		ids := make([]int64, 0, len(members))
		for _, member := range members {
			ids = append(ids, member.FileID)
		}
		facts, err := exe.Lib().ReadFileFacts(ctx, ids, true, true)
		if err != nil {
			return err
		}
		observations, err := exe.ObserveFileRows(ctx, facts)
		if err != nil {
			return err
		}
		rows := make([]keepMember, 0, len(members))
		for _, member := range members {
			observation := observations[member.FileID]
			if observation == nil || !observation.Valid || observation.Entry == nil {
				return fmt.Errorf("group member %d is unavailable or changed: %w", member.FileID, library.ErrLocationConflict)
			}
			ref := observation.Entry.Reference
			if ref.LocationId == req.Keep.GetLocation().LocationId && ref.Path == req.Keep.GetLocation().Path {
				found = true
				continue
			}
			rows = append(rows, keepMember{FileID: member.FileID, LocationID: ref.LocationId, Reference: ref})
			summary.TotalBytes += ref.Facts.SizeBytes
		}
		if len(rows) == 0 {
			return nil
		}
		return stage.Create(&rows).Error
	})
	if err != nil {
		return err
	}
	if !found {
		return ErrKeepNotMember
	}
	if err := stream.Send(&entity.FileOperationResult{Summary: summary}); err != nil {
		return err
	}

	// A dry run reports the copies this keep would remove and stops before any removal.
	if req.GetDryrun() {
		return reportKeepPlan(ctx, stage, summary, stream)
	}

	// Run ordinary bounded Remove requests under the same source gates and aggregate their outcomes.
	aggregate := &keepResults{ResultStream: stream, summary: summary}
	var afterLocation, afterFile int64
	for {
		var rows []keepMember
		if err := stage.Where("location_id > ? OR (location_id = ? AND file_id > ?)", afterLocation, afterLocation, afterFile).Order("location_id, file_id").Limit(256).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		end := 1
		for end < len(rows) && rows[end].LocationID == rows[0].LocationID {
			end++
		}
		rows = rows[:end]
		request := &entity.RemoveFilesRequest{}
		aggregate.fileIDs = make(map[string]int64, len(rows))
		for _, row := range rows {
			request.Sources = append(request.Sources, &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: row.Reference}})
			aggregate.fileIDs[row.Reference.Path] = row.FileID
		}
		aggregate.emitted = 0
		if err := removals.Remove(request, aggregate); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// A preflight failure has no outcomes; after emitted outcomes never replay any mutation.
			if aggregate.emitted != 0 {
				return err
			}
			for _, row := range rows {
				if sendErr := aggregate.Send(&entity.FileOperationResult{Entry: &entity.FileOperationEntry{FileId: &row.FileID, SourcePath: row.Reference.Path, SizeBytes: row.Reference.Facts.SizeBytes, Outcome: entity.FileOperationOutcome_FILE_OPERATION_OUTCOME_FAILED, Error: err.Error()}}); sendErr != nil {
					return sendErr
				}
			}
		}
		last := rows[len(rows)-1]
		afterLocation, afterFile = last.LocationID, last.FileID
	}
	summary.Completed = true
	return stream.Send(&entity.FileOperationResult{Summary: summary})
}

// reportKeepPlan streams the staged non-survivor copies as a report, never as removed entries.
func reportKeepPlan(ctx context.Context, stage *gorm.DB, summary *entity.FileOperationSummary, stream ResultStream) error {
	var afterLocation, afterFile int64
	for {
		var rows []keepMember
		if err := stage.WithContext(ctx).Where("location_id > ? OR (location_id = ? AND file_id > ?)", afterLocation, afterLocation, afterFile).
			Order("location_id, file_id").Limit(256).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			entry := &entity.FileOperationEntry{FileId: &row.FileID, SourcePath: row.Reference.GetPath(),
				SizeBytes: row.Reference.GetFacts().GetSizeBytes(), Outcome: entity.FileOperationOutcome_FILE_OPERATION_OUTCOME_UNPROCESSED}
			if err := stream.Send(&entity.FileOperationResult{Entry: entry, Summary: summary}); err != nil {
				return err
			}
		}
		last := rows[len(rows)-1]
		afterLocation, afterFile = last.LocationID, last.FileID
	}
	summary.Completed = true
	return stream.Send(&entity.FileOperationResult{Summary: summary})
}

func openKeepStage(ctx context.Context, exe *executor.Executor) (*gorm.DB, func() error, error) {
	// Request-owned staging is disposable and never part of Library backup or Job recovery.
	stage, err := resource.OpenTemporaryDB(exe.Paths().Work, "yatm-keep-")
	if err != nil {
		return nil, nil, err
	}
	db := stage.DB.WithContext(ctx)
	if err := db.AutoMigrate(&keepMember{}); err != nil {
		return nil, nil, errors.Join(err, stage.Close())
	}
	return db, stage.Close, nil
}

type keepResults struct {
	ResultStream
	summary *entity.FileOperationSummary
	emitted int
	fileIDs map[string]int64
}

func (s *keepResults) Send(update *entity.FileOperationResult) error {
	// Per-Location summary resets must not appear as completed multi-Location operations.
	if update.Entry == nil {
		return nil
	}
	if id, ok := s.fileIDs[update.Entry.SourcePath]; ok {
		update.Entry.FileId = &id
	}
	s.emitted++
	switch update.Entry.Outcome {
	case entity.FileOperationOutcome_FILE_OPERATION_OUTCOME_SUCCEEDED:
		s.summary.SucceededCount++
		s.summary.UnprocessedCount--
	case entity.FileOperationOutcome_FILE_OPERATION_OUTCOME_FAILED:
		s.summary.FailedCount++
		s.summary.UnprocessedCount--
	case entity.FileOperationOutcome_FILE_OPERATION_OUTCOME_PUBLICATION_PENDING:
		s.summary.PublicationPendingCount++
		s.summary.UnprocessedCount--
	}
	return s.ResultStream.Send(&entity.FileOperationResult{Entry: update.Entry, Summary: s.summary})
}
