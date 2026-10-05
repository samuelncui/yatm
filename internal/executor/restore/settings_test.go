package restore

import (
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestRestoreExecutionSettingsMapToACPOptions(t *testing.T) {
	// Every Job execution setting maps onto the manifest page size or one ACP option: the read
	// buffer, the result queue, the result batch and the result flush interval.
	settings := executionSettingsFrom(&entity.JobExecutionSettings{
		ReadBatch: 4, ReadBufferMax: 16, WriteBufferMax: 8, WriteBatchSize: 4, FlushIntervalMs: 500,
	})
	require.Equal(t, 4, settings.BatchSize)
	require.Equal(t, 16, settings.ReadBufferMax)
	require.Equal(t, 8, settings.ResultBufferMax)
	require.Equal(t, 4, settings.ResultBatchSize)
	require.Equal(t, 500*time.Millisecond, settings.ResultFlushInterval)
}
