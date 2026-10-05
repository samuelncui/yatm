package executor

import (
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestJobResultPageValidatesSharedBounds(t *testing.T) {
	// Every Job manifest listing resolves its page through this one contract.
	page, err := NewJobResultPage(0, "", entity.JobResultOrder_JOB_RESULT_ORDER_ASCENDING, nil, false)
	require.NoError(t, err)
	require.Equal(t, DefaultJobResultPageSize, page.Limit)
	require.Equal(t, "ASC", page.Direction())
	require.Equal(t, ">", page.Comparison())
	require.Equal(t, DefaultJobResultPageSize+1, page.SentinelLimit())
	require.False(t, page.IncludeTotal)
	defaultOrder, err := NewJobResultPage(0, "", entity.JobResultOrder_JOB_RESULT_ORDER_UNSPECIFIED, nil, false)
	require.NoError(t, err)
	require.Equal(t, entity.JobResultOrder_JOB_RESULT_ORDER_ASCENDING, defaultOrder.Order)

	backward, err := NewJobResultPage(7, "42", entity.JobResultOrder_JOB_RESULT_ORDER_DESCENDING, proto.Int64(3), true)
	require.NoError(t, err)
	require.Equal(t, 7, backward.Limit)
	require.Equal(t, "DESC", backward.Direction())
	require.Equal(t, "<", backward.Comparison())
	require.EqualValues(t, 3, backward.Offset)
	require.True(t, backward.IncludeTotal)
	require.True(t, backward.Descending())

	for name, limit := range map[string]int32{"negative": -1, "oversized": MaxJobResultPageSize + 1} {
		_, err := NewJobResultPage(limit, "", entity.JobResultOrder_JOB_RESULT_ORDER_ASCENDING, nil, false)
		require.ErrorContains(t, err, "Job result page limit must be between", name)
	}
	_, err = NewJobResultPage(1, "", entity.JobResultOrder_JOB_RESULT_ORDER_ASCENDING, proto.Int64(-1), false)
	require.ErrorContains(t, err, "offset must not be negative")
	_, err = NewJobResultPage(1, "", entity.JobResultOrder(9), nil, false)
	require.ErrorContains(t, err, "invalid Job result order")
}

func TestJobResultCursorParsing(t *testing.T) {
	// An absent cursor starts the sequence; a malformed one is an error, never a silent reset.
	value, present, err := JobResultCursorID("")
	require.NoError(t, err)
	require.False(t, present)
	require.Zero(t, value)

	value, present, err = JobResultCursorID("12")
	require.NoError(t, err)
	require.True(t, present)
	require.EqualValues(t, 12, value)

	for _, cursor := range []string{"abc", "0", "-4"} {
		_, _, err := JobResultCursorID(cursor)
		require.ErrorContains(t, err, "is not a valid row key", cursor)
	}

	first, second, present, err := JobResultCursorPair(JobResultPairValue(3, 9))
	require.NoError(t, err)
	require.True(t, present)
	require.EqualValues(t, 3, first)
	require.EqualValues(t, 9, second)

	for _, cursor := range []string{"3", "3:", ":9", "a:9"} {
		_, _, _, err := JobResultCursorPair(cursor)
		require.Error(t, err, cursor)
	}
}
