package apis

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const identicalResultIdle = 30 * time.Minute

// Result ownership is process local; active readers keep retired SQLite files open until release.
type identicalResult struct {
	id       string
	snapshot *library.IdenticalSnapshot
	scope    library.IdenticalScope
	lastUsed time.Time
	readers  int
	closed   bool
	timer    *time.Timer
}

type identicalResultManager struct {
	mu       sync.Mutex
	sessions map[string]*identicalResult
	live     int
	building int
	now      func() time.Time
}

func (m *identicalResultManager) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

func (m *identicalResultManager) retireLocked(result *identicalResult) *library.IdenticalSnapshot {
	delete(m.sessions, result.id)
	result.closed = true
	if result.timer != nil {
		result.timer.Stop()
		result.timer = nil
	}
	if result.readers == 0 {
		m.live--
		return result.snapshot
	}
	return nil
}

func (m *identicalResultManager) sweepLocked() []*library.IdenticalSnapshot {
	var closed []*library.IdenticalSnapshot
	now := m.clock()
	for _, result := range m.sessions {
		if result.readers == 0 && !now.Before(result.lastUsed.Add(identicalResultIdle)) {
			closed = append(closed, m.retireLocked(result))
		}
	}
	return closed
}

func closeIdenticalSnapshots(snapshots []*library.IdenticalSnapshot) {
	for _, snapshot := range snapshots {
		closeIdenticalSnapshot(snapshot)
	}
}

func closeIdenticalSnapshot(snapshot *library.IdenticalSnapshot) {
	// Retired result cleanup cannot change already accepted file-operation outcomes.
	if snapshot == nil {
		return
	}
	if err := snapshot.Close(); err != nil {
		logrus.WithError(err).Warn("clean up identical result failed")
	}
}

// reserve owns one of the two result slots before the expensive catalog collection begins.
func (m *identicalResultManager) reserve() error {
	m.mu.Lock()
	closed := m.sweepLocked()
	if m.live+m.building >= 2 {
		var oldest *identicalResult
		for _, result := range m.sessions {
			if result.readers == 0 && (oldest == nil || result.lastUsed.Before(oldest.lastUsed)) {
				oldest = result
			}
		}
		if oldest != nil {
			closed = append(closed, m.retireLocked(oldest))
		}
	}
	if m.live+m.building >= 2 {
		m.mu.Unlock()
		closeIdenticalSnapshots(closed)
		return status.Error(codes.ResourceExhausted, "too many active identical results")
	}
	m.building++
	m.mu.Unlock()
	closeIdenticalSnapshots(closed)
	return nil
}

func (m *identicalResultManager) cancelReservation() {
	m.mu.Lock()
	m.building--
	m.mu.Unlock()
}

// addReserved retains a built snapshot and leases its reserved slot to the creator.
func (m *identicalResultManager) addReserved(snapshot *library.IdenticalSnapshot, scope library.IdenticalScope) *identicalResult {
	m.mu.Lock()
	if m.sessions == nil {
		m.sessions = make(map[string]*identicalResult)
	}
	result := &identicalResult{id: uuid.NewString(), snapshot: snapshot, scope: scope, readers: 1, lastUsed: m.clock()}
	m.sessions[result.id] = result
	m.building--
	m.live++
	m.mu.Unlock()
	return result
}

func (m *identicalResultManager) acquire(id string) (*identicalResult, error) {
	m.mu.Lock()
	closed := m.sweepLocked()
	result := m.sessions[id]
	if result != nil {
		result.readers++
		if result.timer != nil {
			result.timer.Stop()
			result.timer = nil
		}
	}
	m.mu.Unlock()
	closeIdenticalSnapshots(closed)
	if result == nil {
		return nil, status.Error(codes.FailedPrecondition, "identical result expired or unknown")
	}
	return result, nil
}

func (m *identicalResultManager) release(result *identicalResult) {
	m.mu.Lock()
	result.readers--
	if result.closed {
		if result.readers == 0 {
			m.live--
			snapshot := result.snapshot
			m.mu.Unlock()
			closeIdenticalSnapshot(snapshot)
			return
		}
	} else if result.readers == 0 {
		result.lastUsed = m.clock()
		result.timer = time.AfterFunc(identicalResultIdle, func() { m.expire(result.id) })
	}
	m.mu.Unlock()
}

func (m *identicalResultManager) expire(id string) {
	m.mu.Lock()
	result := m.sessions[id]
	var snapshot *library.IdenticalSnapshot
	if result != nil && result.readers == 0 && !m.clock().Before(result.lastUsed.Add(identicalResultIdle)) {
		snapshot = m.retireLocked(result)
	}
	m.mu.Unlock()
	closeIdenticalSnapshot(snapshot)
}

func (m *identicalResultManager) close(id string) error {
	m.mu.Lock()
	closed := m.sweepLocked()
	result := m.sessions[id]
	if result != nil {
		closed = append(closed, m.retireLocked(result))
	}
	m.mu.Unlock()
	closeIdenticalSnapshots(closed)
	if result == nil {
		return status.Error(codes.FailedPrecondition, "identical result expired or unknown")
	}
	return nil
}

// Close releases this process's retained results after request handlers have drained.
func (m *identicalResultManager) Close() error {
	m.mu.Lock()
	var snapshots []*library.IdenticalSnapshot
	for _, result := range m.sessions {
		if snapshot := m.retireLocked(result); snapshot != nil {
			snapshots = append(snapshots, snapshot)
		}
	}
	m.mu.Unlock()

	var failures []error
	for _, snapshot := range snapshots {
		if err := snapshot.Close(); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// Close releases temporary Find results after the HTTP server finishes draining.
func (a *API) Close() error {
	return a.identicalResults.Close()
}

// identicalSnapshot binds a scope to an existing result or creates one for legacy pages.
func (s *filesService) identicalSnapshot(ctx context.Context, id string, scope library.IdenticalScope) (*identicalResult, error) {
	if id == "" {
		if err := s.api.identicalResults.reserve(); err != nil {
			return nil, err
		}
		snapshot, err := s.api.lib.OpenIdenticalSnapshot(ctx, scope)
		if err != nil {
			s.api.identicalResults.cancelReservation()
			return nil, apiError(err)
		}
		return s.api.identicalResults.addReserved(snapshot, scope), nil
	}
	result, err := s.api.identicalResults.acquire(id)
	if err != nil {
		return nil, err
	}
	matches, err := result.snapshot.MatchesScope(scope)
	if err != nil || !matches {
		s.api.identicalResults.release(result)
		return nil, status.Error(codes.InvalidArgument, "identical result scope differs from request")
	}
	return result, nil
}
