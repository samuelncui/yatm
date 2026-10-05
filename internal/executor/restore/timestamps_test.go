package restore

import (
	"encoding/json"
	"math"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
)

func TestRestoreNanosecondsRoundTrip(t *testing.T) {
	// Version cutoffs, output metadata and observed copy health retain exact signed nanoseconds.
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "restore.db"))
	require.NoError(t, err)
	raw, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, raw.Close()) })
	require.NoError(t, db.AutoMigrate(&File{}, &Copy{}))
	last := int64(math.MaxInt64)
	file := &File{ItemID: 1, Path: "file", LastArchivedAtNS: &last, MtimeNS: math.MinInt64}
	copy := &Copy{ID: 1, ItemID: 1, MediaID: 1, MediaPath: "file", HealthCheckedAtNS: 1780000000000123456}
	require.NoError(t, db.Create(file).Error)
	require.NoError(t, db.Create(copy).Error)
	var storedFile File
	var storedCopy Copy
	require.NoError(t, db.First(&storedFile, 1).Error)
	require.NoError(t, db.First(&storedCopy, 1).Error)
	require.Equal(t, file.LastArchivedAtNS, storedFile.LastArchivedAtNS)
	require.Equal(t, file.MtimeNS, storedFile.MtimeNS)
	require.Equal(t, copy.HealthCheckedAtNS, storedCopy.HealthCheckedAtNS)
	data, err := json.Marshal(&storedCopy)
	require.NoError(t, err)
	require.Contains(t, string(data), `"health_checked_at_ns":"1780000000000123456"`)
	var decoded Copy
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, storedCopy.HealthCheckedAtNS, decoded.HealthCheckedAtNS)
	data, err = json.Marshal(&storedFile)
	require.NoError(t, err)
	require.Contains(t, string(data), `"last_archived_at_ns":"9223372036854775807"`)
	require.Contains(t, string(data), `"mtime_ns":"-9223372036854775808"`)
}
