package dataformat

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNanosecondsPreservesInstantsAndUnknownTime(t *testing.T) {
	for _, test := range []struct {
		name string
		time time.Time
		want int64
	}{
		{"unknown", time.Time{}, 0},
		{"epoch", time.Unix(0, 0), 0},
		{"before epoch", time.Unix(-1, 999999999), -1},
		{"whole seconds", time.Unix(1728000000, 0), 1728000000000000000},
		{"milliseconds", time.UnixMilli(1728000000123), 1728000000123000000},
		{"nanoseconds", time.Unix(1728000000, 123456789), 1728000000123456789},
		{"offset", time.Unix(1728000000, 123456789).In(time.FixedZone("source", 7*3600+30*60)), 1728000000123456789},
		{"lower boundary", time.Unix(0, math.MinInt64), math.MinInt64},
		{"upper boundary", time.Unix(0, math.MaxInt64), math.MaxInt64},
	} {
		t.Run(test.name, func(t *testing.T) {
			actual, err := Nanoseconds(test.time)
			require.NoError(t, err)
			require.Equal(t, test.want, actual)
		})
	}
}

func TestNanosecondsRejectsOverflow(t *testing.T) {
	for _, value := range []time.Time{
		time.Unix(0, math.MinInt64).Add(-time.Nanosecond),
		time.Unix(0, math.MaxInt64).Add(time.Nanosecond),
		time.Date(1600, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2400, 1, 1, 0, 0, 0, 0, time.UTC),
	} {
		t.Run(value.Format(time.RFC3339Nano), func(t *testing.T) {
			_, err := Nanoseconds(value)
			require.ErrorContains(t, err, "outside the signed Unix nanosecond range")
		})
	}
}
