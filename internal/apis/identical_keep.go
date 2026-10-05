package apis

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor/fileops"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

// keepMember stages validated physical identities without retaining a large group in memory.
type keepMember struct {
	FileID     int64                    `gorm:"primaryKey;autoIncrement:false"`
	LocationID int64                    `gorm:"index"`
	Reference  *entity.LocationEntryRef `gorm:"serializer:json"`
}

func (s *filesService) KeepIdentical(req *entity.KeepIdenticalRequest, stream entity.FilesService_KeepIdenticalServer) error {
	// Scope and survivor are explicit; every Location is admitted before validating the group.
	scope, err := identicalScope(req.GetScope())
	if err != nil {
		return err
	}
	keep := req.GetKeep().GetLocation()
	if scope.Source != library.IdenticalLocations || keep == nil || req.GetFingerprint() == "" {
		return status.Error(codes.InvalidArgument, "keep requires Locations, one selected survivor and a group fingerprint")
	}
	seed, err := strconv.ParseInt(req.GetGroupId(), 10, 64)
	if err != nil || seed <= 0 {
		return status.Error(codes.InvalidArgument, "invalid identical group ID")
	}
	ids := make([]int64, 0, len(scope.Roots))
	for _, root := range scope.Roots {
		ids = append(ids, root.LocationID)
	}
	err = fileops.WithLocationRemovals(s.api.exe, ids, func(removals *fileops.LocationRemovals) error {
		return s.keepAdmitted(req, scope, seed, removals, fileOperationStream{context: stream.Context(), send: func(result *entity.FileOperationResult) error {
			return stream.Send(&entity.KeepIdenticalResponse{Result: result})
		}})
	})
	return apiError(err)
}

func (s *filesService) keepAdmitted(req *entity.KeepIdenticalRequest, scope library.IdenticalScope, seed int64, removals *fileops.LocationRemovals, stream fileops.ResultStream) (rerr error) {
	// Validate the complete authoritative snapshot before any physical work starts.
	ctx := stream.Context()
	snapshot, err := s.api.lib.OpenIdenticalComponent(ctx, scope, seed)
	if err != nil {
		return err
	}
	defer closeIdenticalSnapshot(snapshot)
	group, err := snapshot.Group(req.GroupId)
	if err != nil {
		return library.ErrLocationConflict
	}
	if group.Fingerprint != req.Fingerprint {
		return library.ErrLocationConflict
	}
	stage, closeStage, err := openKeepStage(ctx)
	if err != nil {
		return err
	}
	defer func() { rerr = errors.Join(rerr, closeStage()) }()
	summary := &entity.FileOperationSummary{TotalItemCount: group.Count - 1, UnprocessedCount: group.Count - 1, Dryrun: req.GetDryrun()}
	found := false
	err = snapshot.WalkMembers(group.ID, func(members []library.IdenticalMember) error {
		_, observations, err := s.memberEntries(ctx, members)
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
		return status.Error(codes.InvalidArgument, "survivor is not a group member")
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
func reportKeepPlan(ctx context.Context, stage *gorm.DB, summary *entity.FileOperationSummary, stream fileops.ResultStream) error {
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

func openKeepStage(ctx context.Context) (*gorm.DB, func() error, error) {
	// Request-owned staging is disposable and never part of Library backup or Job recovery.
	stage, err := resource.OpenTemporaryDB("", "yatm-keep-")
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
	fileops.ResultStream
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
