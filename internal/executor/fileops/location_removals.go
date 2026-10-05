package fileops

import (
	"fmt"
	"sort"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
)

// LocationRemovals scopes one request to the Locations it declared up front.
// It reuses the ordinary Remove planner, object validation and Trash publication.
type LocationRemovals struct {
	service *service
}

// WithLocationRemovals applies the declared Location scope to every bounded Remove batch.
// The callback must not retain the session after returning.
func WithLocationRemovals(exe *executor.Executor, locationIDs []int64, run func(*LocationRemovals) error) error {
	// Deduplicate in stable order so the declared scope is deterministic.
	ids := append([]int64(nil), locationIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	declared := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return fmt.Errorf("Location ID must be positive")
		}
		declared[id] = true
	}

	// A scoped service cannot silently admit additional sources during mutation.
	return run(&LocationRemovals{service: &service{exe: exe, declaredLocations: declared}})
}

func (r *LocationRemovals) Remove(req *entity.RemoveFilesRequest, stream ResultStream) error {
	for _, ref := range req.GetSources() {
		if ref.GetLocation() == nil {
			return fmt.Errorf("Location removal requires physical references")
		}
	}
	return r.service.execute(&entity.FileOperationSpec{Kind: entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE, Sources: req.GetSources()}, req.GetDryrun(), stream)
}

// admitLocation keeps one physical operation inside the Locations its caller declared.
func (s *service) admitLocation(id int64) error {
	if s.declaredLocations == nil {
		return nil
	}
	if !s.declaredLocations[id] {
		return fmt.Errorf("Location was not admitted for this operation")
	}
	return nil
}
