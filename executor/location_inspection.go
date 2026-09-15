package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

// InspectSelections estimates actual selected Location contents without admitting or hashing them.
func (e *Executor) InspectSelections(ctx context.Context, req *entity.InspectSelectionRequest) (*entity.InspectSelectionReply, error) {
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

	// Live backup estimates do not admit content or depend on saved versions.
	request := proto.Clone(req).(*entity.InspectSelectionRequest)
	logical := proto.Clone(req).(*entity.InspectSelectionRequest)
	logical.Selections = nil
	for _, selection := range request.Selections {
		if selection == nil {
			return nil, fmt.Errorf("missing selection")
		}
		if selection.GetLibrary() != nil {
			logical.Selections = append(logical.Selections, selection)
		}
	}
	reply := &entity.InspectSelectionReply{}
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
		entry, err := e.ObserveLocationEntry(ctx, target.LocationId, target.Path)
		if err != nil {
			return nil, err
		}
		ref := entry.Reference
		if target.Reference.BindingToken != ref.BindingToken {
			return nil, library.ErrOnlineConflict
		}
		if target.Reference.Facts != nil {
			ref = target.Reference
		}
		location, _, _, err := e.ResolveLocationEntry(ctx, ref)
		if err != nil {
			return nil, err
		}
		err = e.walkLiveSelection(ctx, location, ref, false, 0, func(ref *entity.LocationEntryRef) error {
			original, err := e.lib.GetFileLocationAtPath(ctx, ref.LocationId, ref.Path)
			if err != nil {
				return err
			}
			if original != nil {
				covered, err := e.selectedLogicalFile(ctx, original.FileID, logical.Selections)
				if err != nil {
					return err
				}
				if covered {
					return nil
				}
			}
			reply.Files++
			reply.Bytes += ref.Facts.Size
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return reply, nil
}

func (e *Executor) coveredPhysicalRoot(ctx context.Context, selections []*entity.FileSelection, index int) (bool, error) {
	target := selections[index].GetLocation()
	location, err := e.lib.GetOnlineSource(ctx, target.LocationId)
	if err != nil {
		return false, err
	}
	if _, err := e.CheckOnlineSource(location); err != nil {
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
			// An explicit ignored file is not covered by recursive selection of its parent.
			if !location.Excluded(target.Path, false) {
				return true, nil
			}
		}
	}
	return false, nil
}

func (e *Executor) selectedLogicalFile(ctx context.Context, fileID int64, selections []*entity.FileSelection) (bool, error) {
	if len(selections) == 0 {
		return false, nil
	}
	parents, err := e.lib.ListParents(ctx, fileID)
	if err != nil {
		return false, err
	}
	for _, selection := range selections {
		target := selection.GetLibrary()
		matches := target.FileId == 0
		for _, parent := range parents {
			if parent.ID == target.FileId {
				matches = true
				break
			}
		}
		if !matches {
			continue
		}
		scope, err := e.lib.ResolveFileScope(ctx, selection.Scope)
		if err != nil {
			return false, err
		}
		if scope != entity.FileScope_FILE_SCOPE_SAVED {
			return true, nil
		}
		_, err = e.lib.LatestFileVersion(ctx, fileID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		return err == nil, err
	}
	return false, nil
}
