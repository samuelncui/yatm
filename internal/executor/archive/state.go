package archive

import (
	"fmt"

	"github.com/samuelncui/yatm/entity"
)

type archiveState uint8

const (
	archiveStateUnspecified archiveState = iota
	archiveStateIndexing
	archiveStateWaitingForMedia
	archiveStateQueued
	archiveStatePreparingMedia
	archiveStateCopying
	archiveStateFinalizingMedia
	archiveStateCompleted
)

var archiveStateTransitions = map[[2]archiveState]struct{}{
	{archiveStatePreparingMedia, archiveStateQueued}:           {},
	{archiveStateQueued, archiveStatePreparingMedia}:           {},
	{archiveStateUnspecified, archiveStateIndexing}:            {},
	{archiveStateIndexing, archiveStateUnspecified}:            {},
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
	case entity.JobStatus_JOB_STATUS_PREPARING:
		return archiveStateUnspecified
	case entity.JobStatus_JOB_STATUS_READY:
		return archiveStateWaitingForMedia
	case entity.JobStatus_JOB_STATUS_COMPLETED:
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
	return a.phaseLocked()
}

// mediaWait reports waiting for a shared Media resource while the physical session is prepared.
func (a *jobArchiveRunner) mediaWait(waiting bool) {
	next := archiveStatePreparingMedia
	if waiting {
		next = archiveStateQueued
	}
	a.lock.Lock()
	current := a.state
	a.lock.Unlock()
	if current == next {
		return
	}
	if err := a.transition(next); err != nil {
		a.logger.WithError(err).Warn("archive Media wait phase transition failed")
	}
}

func (a *jobArchiveRunner) phaseLocked() entity.JobPhase {
	switch a.state {
	case archiveStateIndexing:
		return entity.JobPhase_JOB_PHASE_INDEXING
	case archiveStateQueued:
		return entity.JobPhase_JOB_PHASE_QUEUED
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
