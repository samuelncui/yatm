package scan

import (
	"time"

	"github.com/sirupsen/logrus"
)

// previewMinimumSamples is how many actual decoder completions a Preview estimate needs before its
// rate is meaningful.
const previewMinimumSamples = 3

type previewTiming struct {
	total     int64
	completed int64
	samples   int64
	started   time.Time

	// workTotal is the classified unique generation workload; workKnown says whether the scope
	// classified one at all, so a disabled generator reports counters without a fabricated rate.
	workTotal int64
	workKnown bool
}

// previewOutcomes is one attempt's Preview classification, accumulated over its scopes. It is
// attempt-scoped rather than phase-scoped so the Job keeps reporting it after the phase ends, and
// it is never durable: assets are content-addressed, so the store answers whether a content has a
// Preview and only a failure is worth recording per entry.
type previewOutcomes struct {
	ready   int64
	skipped int64
	failed  int64
}

type previewGeneration struct {
	done chan struct{}
	err  error
}

// beginPreviewGeneration reserves one content or returns the generation every duplicate shares.
func (r *runner) beginPreviewGeneration(signature []byte) (*previewGeneration, bool) {
	r.lock.Lock()
	defer r.lock.Unlock()
	if r.previewContent == nil {
		r.previewContent = map[string]*previewGeneration{}
	}
	key := string(signature)
	if generation := r.previewContent[key]; generation != nil {
		return generation, false
	}
	generation := &previewGeneration{done: make(chan struct{})}
	r.previewContent[key] = generation
	return generation, true
}

func (r *runner) finishPreviewGeneration(generation *previewGeneration, err error) {
	r.lock.Lock()
	defer r.lock.Unlock()
	generation.err = err
	close(generation.done)
}

func (r *runner) logInfo(message string, fields logrus.Fields) {
	if r.logger == nil {
		return
	}
	if r.job != nil {
		fields["job_id"] = r.job.ID
	}
	r.logger.WithFields(fields).Info(message)
}

func (r *runner) logResult(message string, started time.Time, err error, fields logrus.Fields) {
	if r.logger == nil {
		return
	}
	if !started.IsZero() {
		fields["elapsed_ms"] = time.Since(started).Milliseconds()
	}
	if r.job != nil {
		fields["job_id"] = r.job.ID
	}
	entry := r.logger.WithFields(fields)
	if err != nil {
		entry.WithError(err).Warn(message)
		return
	}
	entry.Info(message)
}
