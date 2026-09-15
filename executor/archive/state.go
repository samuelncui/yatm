package archive

import (
	"fmt"

	"github.com/samuelncui/yatm/entity"
)

type archiveState uint8

const (
	archiveStateUnspecified archiveState = iota
	archiveStateWaitingForIndexRetry
	archiveStateIndexing
	archiveStateWaitingForMedia
	archiveStatePreparingMedia
	archiveStateCopying
	archiveStateFinalizingMedia
	archiveStateCompleted
)

var archiveStateTransitions = map[[2]archiveState]struct{}{
	{archiveStateWaitingForIndexRetry, archiveStateIndexing}:   {},
	{archiveStateIndexing, archiveStateWaitingForIndexRetry}:   {},
	{archiveStateIndexing, archiveStateWaitingForMedia}:        {},
	{archiveStateWaitingForMedia, archiveStatePreparingMedia}:  {},
	{archiveStatePreparingMedia, archiveStateCopying}:          {},
	{archiveStatePreparingMedia, archiveStateWaitingForMedia}:  {},
	{archiveStateCopying, archiveStateFinalizingMedia}:         {},
	{archiveStateCopying, archiveStateWaitingForMedia}:         {},
	{archiveStateFinalizingMedia, archiveStateWaitingForMedia}: {},
	{archiveStateFinalizingMedia, archiveStateCompleted}:       {},
}

func newArchiveState(status entity.JobStatus) archiveState {
	switch status {
	case entity.JobStatus_INDEXING:
		return archiveStateWaitingForIndexRetry
	case entity.JobStatus_PENDING:
		return archiveStateWaitingForMedia
	case entity.JobStatus_COMPLETED:
		return archiveStateCompleted
	default:
		return archiveStateUnspecified
	}
}

func (a *jobArchiveRunner) transition(next archiveState) error {
	a.lock.Lock()
	current := a.state
	if _, allowed := archiveStateTransitions[[2]archiveState{current, next}]; !allowed {
		a.lock.Unlock()
		return fmt.Errorf("invalid archive state transition, from=%d to=%d", current, next)
	}
	a.state = next
	a.lock.Unlock()
	a.exe.TouchJob(a.job.ID)
	return nil
}

func (a *jobArchiveRunner) Phase() entity.JobPhase {
	a.lock.Lock()
	defer a.lock.Unlock()

	switch a.state {
	case archiveStateWaitingForIndexRetry:
		return entity.JobPhase_JOB_PHASE_WAITING_FOR_INDEX_RETRY
	case archiveStateIndexing:
		return entity.JobPhase_JOB_PHASE_INDEXING
	case archiveStateWaitingForMedia:
		return entity.JobPhase_JOB_PHASE_WAITING_FOR_MEDIA
	case archiveStatePreparingMedia:
		return entity.JobPhase_JOB_PHASE_PREPARING_MEDIA
	case archiveStateCopying:
		return entity.JobPhase_JOB_PHASE_COPYING_TO_MEDIA
	case archiveStateFinalizingMedia:
		return entity.JobPhase_JOB_PHASE_FINALIZING_MEDIA
	case archiveStateCompleted:
		return entity.JobPhase_JOB_PHASE_COMPLETED
	default:
		return entity.JobPhase_JOB_PHASE_UNSPECIFIED
	}
}
