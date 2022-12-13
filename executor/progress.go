package executor

import (
	"sync/atomic"
	"time"
)

const SpeedLen = 30

type speedEvent struct {
	bytes int64
	time  time.Time
}

type progress struct {
	speedEvents []speedEvent
	speedLen    int
	speedIdx    int

	startTime time.Time
	speed     int64

	totalBytes, totalFiles int64
	bytes, files           int64
}

func newProgress() *progress {
	return &progress{speedEvents: make([]speedEvent, SpeedLen), speedLen: SpeedLen, startTime: time.Now()}
}

func (p *progress) setBytes(bytes int64) {
	atomic.StoreInt64(&p.bytes, bytes)
	now := time.Now()

	p.speedEvents[p.speedIdx] = speedEvent{bytes: bytes, time: now}
	for earliest := p.speedIdx; ; {
		earliest++
		if earliest >= p.speedLen {
			earliest = 0
		}
		if earliest == p.speedIdx {
			break
		}

		if !p.speedEvents[earliest].time.IsZero() {
			p.speed = (bytes - p.speedEvents[earliest].bytes) * 1e9 / now.Sub(p.speedEvents[earliest].time).Nanoseconds()
			break
		}
	}

	p.speedIdx++
	if p.speedIdx >= p.speedLen {
		p.speedIdx = 0
	}
}
