package restore

import (
	"context"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
)

// executionSettings is the Restore attempt's pipeline configuration. The runner resolves the
// operator's settings once per attempt and maps each one onto the manifest page size or one ACP
// option: the read buffer, the result queue, the result batch and the result flush interval.
type executionSettings struct {
	BatchSize           int
	ReadBufferMax       int
	ResultBufferMax     int
	ResultBatchSize     int
	ResultFlushInterval time.Duration
}

// executionSettings reads the Job execution settings this attempt runs with. A stored group that
// cannot run is an explicit error, never a silent fallback to defaults the operator did not choose.
func (a *jobRestoreRunner) executionSettings(ctx context.Context) (executionSettings, error) {
	settings, err := executor.JobExecutionSettings(ctx)
	if err != nil {
		return executionSettings{}, err
	}
	return executionSettingsFrom(settings), nil
}

func executionSettingsFrom(settings *entity.JobExecutionSettings) executionSettings {
	return executionSettings{
		BatchSize:           int(settings.GetReadBatch()),
		ReadBufferMax:       int(settings.GetReadBufferMax()),
		ResultBufferMax:     int(settings.GetWriteBufferMax()),
		ResultBatchSize:     int(settings.GetWriteBatchSize()),
		ResultFlushInterval: time.Duration(settings.GetFlushIntervalMs()) * time.Millisecond,
	}
}
