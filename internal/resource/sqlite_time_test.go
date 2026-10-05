package resource

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSQLiteTimestampRoundTrip(t *testing.T) {
	// The supported legacy reader must preserve time.Time values, including source timezone offsets.
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "time.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closeDB(db)) })
	type timestamp struct {
		ID    int
		Value time.Time
	}
	require.NoError(t, db.AutoMigrate(&timestamp{}))
	values := []time.Time{{}, time.Now(), time.Unix(1, 123).In(time.FixedZone("+0730", 27000)),
		time.Date(2026, 10, 3, 4, 5, 6, 123456789, time.FixedZone("+08", 8*3600))}
	for i, value := range values {
		require.NoError(t, db.Create(&timestamp{ID: i + 1, Value: value}).Error)
	}

	// Raw text is diagnostic evidence if the selected driver cannot decode its own encoding.
	for i, want := range values {
		var text string
		require.NoError(t, db.Raw("SELECT CAST(value AS TEXT) FROM timestamps WHERE id = ?", i+1).Scan(&text).Error)
		var got timestamp
		require.NoError(t, db.First(&got, i+1).Error, "stored timestamp: %q", text)
		require.True(t, want.Equal(got.Value), "want %s, got %s", want, got.Value)
	}
}

func TestSQLiteNanosecondIntegerRoundTrip(t *testing.T) {
	// Current persistence uses signed integers rather than the legacy driver's text date encoding.
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "nanoseconds.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closeDB(db)) })
	type instant struct {
		ID         int
		AtNS       int64
		OptionalNS *int64
	}
	require.NoError(t, db.AutoMigrate(&instant{}))
	for index, stamp := range []int64{math.MinInt64, -1, 0, 1791000000123456781, math.MaxInt64} {
		want := instant{ID: index + 1, AtNS: stamp, OptionalNS: new(stamp)}
		require.NoError(t, db.Create(&want).Error)
		var actual instant
		require.NoError(t, db.First(&actual, want.ID).Error)
		require.Equal(t, want, actual)
		var storage string
		require.NoError(t, db.Raw("SELECT typeof(at_ns) FROM instants WHERE id = ?", want.ID).Scan(&storage).Error)
		require.Equal(t, "integer", storage)
	}

	// An absent optional instant is SQL NULL, distinct from an explicitly recorded zero.
	require.NoError(t, db.Create(&instant{ID: 6}).Error)
	var actual instant
	require.NoError(t, db.First(&actual, 6).Error)
	require.Nil(t, actual.OptionalNS)
}
