package preview

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/previewprotocol"
)

var ErrDisabled = errors.New("Preview generation is disabled")

type generation struct {
	done     chan struct{}
	err      error
	identity string
}

func (m *Manager) currentSettings(ctx context.Context) (*entity.PreviewSettings, error) {
	if m.settings != nil {
		return m.settings(ctx)
	}
	return &entity.PreviewSettings{Enabled: true, Concurrency: 2, TimeoutSeconds: 600, MaxInputPixels: 64_000_000}, nil
}

func (m *Manager) beginGeneration(ctx context.Context, key, sourcePath, identity string) (*generation, bool, error) {
	// Serialize publication for one content key, sharing only a successful compatible generation.
	var pending *generation
	for {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		m.lock.Lock()
		if previous := m.pending[key]; previous != nil {
			m.lock.Unlock()
			select {
			case <-previous.done:
			case <-ctx.Done():
				return nil, false, ctx.Err()
			}
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
			if previous.identity == identity && previous.err == nil {
				return previous, false, nil
			}
			continue
		}
		if len(m.pending) >= 64 {
			m.lock.Unlock()
			return nil, false, fmt.Errorf("Preview queue is full")
		}
		pending = &generation{done: make(chan struct{}), identity: identity}
		m.pending[key] = pending
		m.lock.Unlock()
		break
	}
	reportProgress(ctx, previewprotocol.Event{Type: "progress", Phase: "queued"})

	// Recheck live enablement after waiting; reducing capacity never interrupts active files.
	for {
		settings, err := m.currentSettings(ctx)
		if err == nil && (!settings.GetEnabled() || (m.settings != nil && !supportsRoute(sourcePath, settings.GetGenerators()))) {
			err = ErrDisabled
		}
		if err == nil && (settings.GetConcurrency() < 1 || settings.GetConcurrency() > 16) {
			err = fmt.Errorf("invalid Preview concurrency")
		}
		if err == nil {
			err = ctx.Err()
		}
		if err != nil {
			m.finishGeneration(key, pending, false, err)
			return nil, false, err
		}
		m.lock.Lock()
		if m.active < int(settings.GetConcurrency()) {
			m.active++
			m.lock.Unlock()
			return pending, true, nil
		}
		changed := m.changed
		m.lock.Unlock()
		select {
		case <-ctx.Done():
			m.finishGeneration(key, pending, false, ctx.Err())
			return nil, false, ctx.Err()
		case <-changed:
		case <-time.After(time.Second):
		}
	}
}

func (m *Manager) finishGeneration(key string, pending *generation, active bool, err error) {
	// Publish one shared result and release capacity only after output cleanup/publication finishes.
	m.lock.Lock()
	defer m.lock.Unlock()
	pending.err = err
	delete(m.pending, key)
	if active {
		m.active--
	}
	close(pending.done)
	close(m.changed)
	m.changed = make(chan struct{})
}
