package executor

import (
	"context"
	"fmt"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
)

// InspectSelections estimates actual selected Location contents without admitting or hashing them.
func (e *Executor) InspectSelections(ctx context.Context, req *entity.SelectionInspection) (*entity.SelectionInspectionResult, error) {
	// Keep restore metadata inspection and logical scope filtering in Library.
	if req == nil || len(req.Selections)+len(req.FileVersionIds) == 0 || len(req.Selections)+len(req.FileVersionIds) > 1000 {
		return nil, fmt.Errorf("inspect between 1 and 1000 roots")
	}
	if req.Restore {
		return e.lib.InspectSelection(ctx, req)
	}
	if req.VersionPolicy != nil || req.SkipUnmatchedVersions {
		return nil, fmt.Errorf("version policy requires a Restore selection")
	}

	// Live Archive estimates do not admit content or depend on saved versions.
	request := proto.Clone(req).(*entity.SelectionInspection)
	logical := proto.Clone(req).(*entity.SelectionInspection)
	logical.Selections = nil
	for _, selection := range request.Selections {
		if selection == nil {
			return nil, fmt.Errorf("missing selection")
		}
		if selection.GetLibrary() != nil {
			logical.Selections = append(logical.Selections, selection)
		}
	}
	reply := &entity.SelectionInspectionResult{}
	if len(logical.Selections)+len(logical.FileVersionIds) > 0 {
		var err error
		reply, err = e.lib.InspectSelection(ctx, logical)
		if err != nil {
			return nil, err
		}
	}
	if len(request.Selections) > 0 {
		if err := e.lib.FreezeSelections(ctx, request.Selections); err != nil {
			return nil, err
		}
	}
	reply.Selections = request.Selections

	// Only explicit roots are retained; descendant deduplication uses bounded ancestry checks.
	for index, selection := range request.Selections {
		target := selection.GetLocation()
		if target == nil {
			continue
		}
		covered, err := e.coveredPhysicalRoot(ctx, request.Selections, index)
		if err != nil {
			return nil, err
		}
		if covered {
			continue
		}
		location, _, info, err := e.ResolveLocationEntry(ctx, &entity.LocationEntryRef{
			LocationId: target.LocationId, Path: target.Path,
		})
		if err != nil {
			return nil, err
		}
		entry, err := locationEntry(location.ID, target.Path, info)
		if err != nil {
			return nil, err
		}
		ref := entry.Reference
		// Resolve associations and logical overlap once per physical batch.
		batch := make([]*entity.LocationEntryRef, 0, 256)
		flush := func() error {
			if len(batch) == 0 {
				return nil
			}
			paths := make([]string, 0, len(batch))
			for _, row := range batch {
				paths = append(paths, row.Path)
			}
			originals, err := e.lib.ReadFileOriginalsAt(ctx, location.ID, paths)
			if err != nil {
				return err
			}
			ids := make([]int64, 0, len(originals))
			for _, original := range originals {
				ids = append(ids, original.FileID)
			}
			covered, err := e.lib.MatchSelectionFiles(ctx, ids, logical.Selections)
			if err != nil {
				return err
			}
			for _, row := range batch {
				if original := originals[row.Path]; original != nil && covered[original.FileID] {
					continue
				}
				reply.FileCount++
				reply.TotalBytes += row.Facts.SizeBytes
			}
			batch = batch[:0]
			return nil
		}
		err = e.walkLiveSelection(ctx, location, ref, false, 0, func(row *entity.LocationEntryRef) error {
			batch = append(batch, row)
			if len(batch) == cap(batch) {
				return flush()
			}
			return nil
		})
		if err == nil {
			err = flush()
		}
		if err != nil {
			return nil, err
		}
	}
	return reply, nil
}

func (e *Executor) coveredPhysicalRoot(ctx context.Context, selections []*entity.FileSelection, index int) (bool, error) {
	target := selections[index].GetLocation()
	location, err := e.lib.GetLocation(ctx, target.LocationId)
	if err != nil {
		return false, err
	}
	if _, err := e.CheckLocation(location); err != nil {
		return false, err
	}
	for otherIndex, selection := range selections {
		other := selection.GetLocation()
		if other == nil || otherIndex == index || other.LocationId != target.LocationId {
			continue
		}
		if other.Path == target.Path {
			if otherIndex < index {
				return true, nil
			}
			continue
		}
		if other.Path == "" || strings.HasPrefix(target.Path, other.Path+"/") {
			// Selection expansion prunes Ignore, so a covered root never adds excluded content.
			return true, nil
		}
	}
	return false, nil
}
