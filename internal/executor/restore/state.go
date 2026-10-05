package restore

import (
	"fmt"

	"github.com/samuelncui/yatm/entity"
)

type restoreState uint8

const (
	restoreStateUnspecified restoreState = iota
	restoreStateIndexing
	restoreStateWaitingForMedia
	restoreStateQueued
	restoreStatePreparingMedia
	restoreStateCopying
	restoreStateFinalizingMedia
	restoreStateCompleted
)

var restoreStateTransitions = map[[2]restoreState]struct{}{
	{restoreStatePreparingMedia, restoreStateQueued}:           {},
	{restoreStateQueued, restoreStatePreparingMedia}:           {},
	{restoreStateUnspecified, restoreStateIndexing}:            {},
	{restoreStateIndexing, restoreStateUnspecified}:            {},
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
	case entity.JobStatus_JOB_STATUS_PREPARING:
		return restoreStateUnspecified
	case entity.JobStatus_JOB_STATUS_READY:
		return restoreStateWaitingForMedia
	case entity.JobStatus_JOB_STATUS_COMPLETED:
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
	return a.phaseLocked()
}

// mediaWait reports waiting for a shared Media resource while the physical session is prepared.
func (a *jobRestoreRunner) mediaWait(waiting bool) {
	next := restoreStatePreparingMedia
	if waiting {
		next = restoreStateQueued
	}
	a.lock.Lock()
	current := a.state
	a.lock.Unlock()
	if current == next {
		return
	}
	if err := a.transition(next); err != nil {
		a.logger.WithError(err).Warn("restore Media wait phase transition failed")
	}
}

func (a *jobRestoreRunner) phaseLocked() entity.JobPhase {
	switch a.state {
	case restoreStateIndexing:
		return entity.JobPhase_JOB_PHASE_INDEXING
	case restoreStateQueued:
		return entity.JobPhase_JOB_PHASE_QUEUED
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
