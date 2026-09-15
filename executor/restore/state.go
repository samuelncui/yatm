package restore

import (
	"fmt"

	"github.com/samuelncui/yatm/entity"
)

type restoreState uint8

const (
	restoreStateUnspecified restoreState = iota
	restoreStateWaitingForIndexRetry
	restoreStateIndexing
	restoreStateWaitingForMedia
	restoreStatePreparingMedia
	restoreStateCopying
	restoreStateFinalizingMedia
	restoreStateCompleted
)

var restoreStateTransitions = map[[2]restoreState]struct{}{
	{restoreStateWaitingForIndexRetry, restoreStateIndexing}:   {},
	{restoreStateIndexing, restoreStateWaitingForIndexRetry}:   {},
	{restoreStateIndexing, restoreStateWaitingForMedia}:        {},
	{restoreStateIndexing, restoreStateCompleted}:              {},
	{restoreStateWaitingForMedia, restoreStatePreparingMedia}:  {},
	{restoreStatePreparingMedia, restoreStateCopying}:          {},
	{restoreStatePreparingMedia, restoreStateWaitingForMedia}:  {},
	{restoreStatePreparingMedia, restoreStateCompleted}:        {},
	{restoreStateCopying, restoreStateFinalizingMedia}:         {},
	{restoreStateCopying, restoreStateWaitingForMedia}:         {},
	{restoreStateFinalizingMedia, restoreStateWaitingForMedia}: {},
	{restoreStateFinalizingMedia, restoreStateCompleted}:       {},
}

func newRestoreState(status entity.JobStatus) restoreState {
	switch status {
	case entity.JobStatus_INDEXING:
		return restoreStateWaitingForIndexRetry
	case entity.JobStatus_PENDING:
		return restoreStateWaitingForMedia
	case entity.JobStatus_COMPLETED:
		return restoreStateCompleted
	default:
		return restoreStateUnspecified
	}
}

func (a *jobRestoreRunner) transition(next restoreState) error {
	a.lock.Lock()
	current := a.state
	if _, allowed := restoreStateTransitions[[2]restoreState{current, next}]; !allowed {
		a.lock.Unlock()
		return fmt.Errorf("invalid restore state transition, from=%d to=%d", current, next)
	}
	a.state = next
	a.lock.Unlock()
	a.exe.TouchJob(a.job.ID)
	return nil
}

func (a *jobRestoreRunner) Phase() entity.JobPhase {
	a.lock.Lock()
	defer a.lock.Unlock()

	switch a.state {
	case restoreStateWaitingForIndexRetry:
		return entity.JobPhase_JOB_PHASE_WAITING_FOR_INDEX_RETRY
	case restoreStateIndexing:
		return entity.JobPhase_JOB_PHASE_INDEXING
	case restoreStateWaitingForMedia:
		return entity.JobPhase_JOB_PHASE_WAITING_FOR_MEDIA
	case restoreStatePreparingMedia:
		return entity.JobPhase_JOB_PHASE_PREPARING_MEDIA
	case restoreStateCopying:
		return entity.JobPhase_JOB_PHASE_COPYING_FROM_MEDIA
	case restoreStateFinalizingMedia:
		return entity.JobPhase_JOB_PHASE_FINALIZING_MEDIA
	case restoreStateCompleted:
		return entity.JobPhase_JOB_PHASE_COMPLETED
	default:
		return entity.JobPhase_JOB_PHASE_UNSPECIFIED
	}
}
