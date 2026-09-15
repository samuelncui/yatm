package executor

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/samuelncui/yatm/entity"
)

const SpeedLen = 30

type speedEvent struct {
	bytes int64
	time  time.Time
}

type Progress struct {
	startTime time.Time
	now       func() time.Time

	// atomic fields
	totalBytes, totalFiles        int64
	baseBytes, baseFiles          int64
	sessionBytes, sessionFiles    int64
	sessionStartedAt              int64
	historicalBytes               int64
	historicalDurationNanoseconds int64
	speed                         int64

	// locked fields
	lock        sync.Mutex
	speedEvents []speedEvent
	speedLen    int
	speedIdx    int
}

func NewProgress() *Progress {
	return &Progress{
		startTime:   time.Now(),
		now:         time.Now,
		speedEvents: make([]speedEvent, SpeedLen),
		speedLen:    SpeedLen,
	}
}

func (p *Progress) ToEntity() *entity.Progress {
	now := p.now()
	sessionBytes := atomic.LoadInt64(&p.sessionBytes)
	sessionDuration := time.Duration(0)
	if startedAt := atomic.LoadInt64(&p.sessionStartedAt); startedAt > 0 {
		sessionDuration = now.Sub(time.Unix(0, startedAt))
	}
	historicalBytes := atomic.LoadInt64(&p.historicalBytes) + sessionBytes
	historicalDuration := time.Duration(atomic.LoadInt64(&p.historicalDurationNanoseconds)) + sessionDuration
	return &entity.Progress{
		TotalBytes:             atomic.LoadInt64(&p.totalBytes),
		TotalFiles:             atomic.LoadInt64(&p.totalFiles),
		CopiedBytes:            atomic.LoadInt64(&p.baseBytes) + sessionBytes,
		CopiedFiles:            atomic.LoadInt64(&p.baseFiles) + atomic.LoadInt64(&p.sessionFiles),
		Speed:                  atomic.LoadInt64(&p.speed),
		StartTime:              p.startTime.Unix(),
		AverageSpeed:           bytesPerSecond(sessionBytes, sessionDuration),
		HistoricalAverageSpeed: bytesPerSecond(historicalBytes, historicalDuration),
	}
}

func (p *Progress) SetGlobalTotal(bytes, files int64) {
	atomic.StoreInt64(&p.totalBytes, bytes)
	atomic.StoreInt64(&p.totalFiles, files)
}

func (p *Progress) SetGlobalCopied(bytes, files int64) {
	atomic.StoreInt64(&p.baseBytes, bytes)
	atomic.StoreInt64(&p.baseFiles, files)
}

func (p *Progress) StartSession() {
	atomic.CompareAndSwapInt64(&p.sessionStartedAt, 0, p.now().UnixNano())
}

func (p *Progress) UpdateSessionCurrent(bytes, files int64) {
	now := p.now()
	p.StartSession()
	atomic.StoreInt64(&p.sessionBytes, bytes)
	atomic.StoreInt64(&p.sessionFiles, files)

	// Derive the moving speed from the bounded event ring.
	p.lock.Lock()
	defer p.lock.Unlock()

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
			atomic.StoreInt64(&p.speed, bytesPerSecond(
				bytes-p.speedEvents[earliest].bytes,
				now.Sub(p.speedEvents[earliest].time),
			))
			break
		}
	}

	p.speedIdx++
	if p.speedIdx >= p.speedLen {
		p.speedIdx = 0
	}
}

func (p *Progress) CommitSession() {
	p.lock.Lock()
	defer p.lock.Unlock()

	// Move the completed attempt into the global counters and observed throughput history.
	bytes := atomic.SwapInt64(&p.sessionBytes, 0)
	atomic.AddInt64(&p.baseBytes, bytes)
	atomic.AddInt64(&p.baseFiles, atomic.SwapInt64(&p.sessionFiles, 0))
	if startedAt := atomic.SwapInt64(&p.sessionStartedAt, 0); startedAt > 0 {
		duration := p.now().Sub(time.Unix(0, startedAt))
		if duration > 0 && bytes > 0 {
			atomic.AddInt64(&p.historicalBytes, bytes)
			atomic.AddInt64(&p.historicalDurationNanoseconds, duration.Nanoseconds())
		}
	}
	atomic.StoreInt64(&p.speed, 0)

	// Clear the attempt-local moving-speed window.
	for i := range p.speedEvents {
		p.speedEvents[i] = speedEvent{}
	}
	p.speedIdx = 0
}

func bytesPerSecond(bytes int64, duration time.Duration) int64 {
	if bytes <= 0 || duration <= 0 {
		return 0
	}
	return int64(float64(bytes) / duration.Seconds())
}
