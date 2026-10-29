package media

import (
	"testing"

	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/stretchr/testify/require"
)

func TestTarCapacityClosureReserveFollowsPhysicalRecordMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode tape.RecordMode
		want uint64
	}{
		{"variable short", tape.RecordMode{MaximumBytes: 4096}, 1344},
		{"fixed padded", tape.RecordMode{MaximumBytes: 4096, FixedBytes: 4096}, 4992},
		{"variable blocks", tape.RecordMode{MaximumBytes: 512}, 1408},
		{"fixed blocks", tape.RecordMode{MaximumBytes: 512, FixedBytes: 512}, 1408},
		{"large variable", tape.RecordMode{MaximumBytes: 16*1024*1024 - 512}, 1344},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Variable records emit actual short bytes; only fixed mode needs final record padding.
			budget, err := newTarCapacityBudget(tarCapacityTestOptions(100000000), tc.mode)
			require.NoError(t, err)
			require.Equal(t, tc.want, budget.closure)
		})
	}
}
