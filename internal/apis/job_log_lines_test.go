package apis

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func testLogFile(t *testing.T, content string) *os.File {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "job-log-*")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, file.Close()) })
	_, err = file.WriteString(content)
	require.NoError(t, err)
	return file
}

func TestJobLogLinesTailBeyondRawReadLimit(t *testing.T) {
	// A settled Job's last failure must be the first page regardless of its log size.
	content := strings.Repeat("time=2026-09-24T00:00:00Z level=info msg=work\n", 100000) +
		"time=2026-09-24T00:01:00Z level=error msg=last failure\n"
	file := testLogFile(t, content)
	reply, err := listOlderLogLines(file, int64(len(content)), &entity.ListJobLogLinesRequest{})
	require.NoError(t, err)
	require.NotEmpty(t, reply.Lines)
	require.Contains(t, reply.Lines[len(reply.Lines)-1].Text, "last failure")
	require.Equal(t, "error", reply.Lines[len(reply.Lines)-1].Level)
	require.True(t, reply.HasOlder)
	require.Equal(t, int64(len(content)), reply.AfterCursor)
}

func TestJobLogLinesPagesBothDirectionsWithoutDuplicates(t *testing.T) {
	// Walk more than one page in each direction and compare exact row identities.
	var content strings.Builder
	for i := range 501 {
		fmt.Fprintf(&content, "level=info msg=line-%03d\n", i)
	}
	file := testLogFile(t, content.String())
	size := int64(content.Len())
	for _, direction := range []entity.JobLogDirection{
		entity.JobLogDirection_JOB_LOG_DIRECTION_OLDER,
		entity.JobLogDirection_JOB_LOG_DIRECTION_NEWER,
	} {
		var cursor *int64
		seen := make(map[int64]bool)
		for {
			req := &entity.ListJobLogLinesRequest{Direction: direction, Cursor: cursor}
			var reply *entity.ListJobLogLinesResponse
			var err error
			if direction == entity.JobLogDirection_JOB_LOG_DIRECTION_OLDER {
				reply, err = listOlderLogLines(file, size, req)
			} else {
				reply, err = listNewerLogLines(file, size, req)
			}
			require.NoError(t, err)
			for _, line := range reply.Lines {
				require.False(t, seen[line.Offset], "duplicate line at %d", line.Offset)
				seen[line.Offset] = true
			}
			if direction == entity.JobLogDirection_JOB_LOG_DIRECTION_OLDER {
				if !reply.HasOlder {
					break
				}
				cursor = &reply.BeforeCursor
			} else {
				if !reply.HasNewer {
					break
				}
				cursor = &reply.AfterCursor
			}
		}
		require.Len(t, seen, 501)
	}
}

func TestJobLogLinesFilteredEmptyPageAdvances(t *testing.T) {
	// A filter with no match in one scan window still moves toward older bytes.
	content := "level=error msg=needle\n" + strings.Repeat("level=info msg=other\n", 60000)
	file := testLogFile(t, content)
	req := &entity.ListJobLogLinesRequest{Level: "error", Query: "NEEDLE"}
	reply, err := listOlderLogLines(file, int64(len(content)), req)
	require.NoError(t, err)
	require.Empty(t, reply.Lines)
	require.True(t, reply.HasOlder)
	require.Less(t, reply.BeforeCursor, int64(len(content)))
	req.Cursor = &reply.BeforeCursor
	reply, err = listOlderLogLines(file, int64(len(content)), req)
	require.NoError(t, err)
	require.Len(t, reply.Lines, 1)
	require.Equal(t, "error", reply.Lines[0].Level)
}

func TestJobLogLinesLongUTF8AndGrowingTail(t *testing.T) {
	// A long line makes byte progress without producing invalid protobuf text.
	long := strings.Repeat("界", jobLogScanBytes/3+10) + "\n"
	file := testLogFile(t, long+"level=info msg=half")
	first, err := listNewerLogLines(file, int64(len(long)+len("level=info msg=half")), &entity.ListJobLogLinesRequest{})
	require.NoError(t, err)
	require.Len(t, first.Lines, 1)
	require.True(t, utf8.ValidString(first.Lines[0].Text))
	require.True(t, first.HasNewer)
	require.Greater(t, first.AfterCursor, int64(0))

	// The next page completes the long line, then observes a partial line that later grows.
	cursor := first.AfterCursor
	second, err := listNewerLogLines(file, int64(len(long)+len("level=info msg=half")), &entity.ListJobLogLinesRequest{Cursor: &cursor})
	require.NoError(t, err)
	require.True(t, second.Lines[0].Continuation)
	require.Equal(t, "level=info msg=half", second.Lines[len(second.Lines)-1].Text)
	require.False(t, second.Lines[len(second.Lines)-1].Complete)
	_, err = file.WriteString(" done\n")
	require.NoError(t, err)
	cursor = second.AfterCursor
	third, err := listNewerLogLines(file, int64(len(long)+len("level=info msg=half done\n")), &entity.ListJobLogLinesRequest{Cursor: &cursor})
	require.NoError(t, err)
	require.Len(t, third.Lines, 1)
	require.True(t, third.Lines[0].Continuation)
	require.Equal(t, " done", third.Lines[0].Text)
	require.True(t, third.Lines[0].Complete)
}

func TestJobLogLinesFilteredGrowingLineKeepsContinuation(t *testing.T) {
	// A filtered partial line must keep the bytes appended after the first read.
	content := "level=error msg=needle"
	file := testLogFile(t, content)
	req := &entity.ListJobLogLinesRequest{Level: "error", Query: "needle"}
	first, err := listNewerLogLines(file, int64(len(content)), req)
	require.NoError(t, err)
	require.Len(t, first.Lines, 1)
	require.False(t, first.Lines[0].Complete)

	// The byte cursor begins inside the same physical line, beyond its level and query text.
	appended := " more detail\n"
	_, err = file.WriteString(appended)
	require.NoError(t, err)
	cursor := first.AfterCursor
	req.Cursor = &cursor
	second, err := listNewerLogLines(file, int64(len(content+appended)), req)
	require.NoError(t, err)
	require.Len(t, second.Lines, 1)
	require.True(t, second.Lines[0].Continuation)
	require.Empty(t, second.Lines[0].Level)
	require.Equal(t, appended[:len(appended)-1], second.Lines[0].Text)
	require.True(t, second.Lines[0].Complete)
	require.Equal(t, int64(len(content+appended)), second.AfterCursor)
}

func TestJobLogLinesFilteredLongLineKeepsBoundaryFragments(t *testing.T) {
	// Each read remains bounded while a line crosses the scan window and the filter text appears
	// in only one fragment.
	content := "level=error msg=needle " + strings.Repeat("a", jobLogScanBytes) + " boundary\n" +
		"level=info msg=unrelated\n"
	file := testLogFile(t, content)
	req := &entity.ListJobLogLinesRequest{Level: "error", Query: "needle"}
	first, err := listNewerLogLines(file, int64(len(content)), req)
	require.NoError(t, err)
	require.Len(t, first.Lines, 1)
	require.True(t, first.HasNewer)
	require.Equal(t, int64(jobLogScanBytes), first.AfterCursor)

	// The later fragment has neither level nor query, but belongs to the filtered line.
	cursor := first.AfterCursor
	req.Cursor = &cursor
	second, err := listNewerLogLines(file, int64(len(content)), req)
	require.NoError(t, err)
	require.Len(t, second.Lines, 1)
	require.True(t, second.Lines[0].Continuation)
	require.Empty(t, second.Lines[0].Level)
	require.True(t, second.Lines[0].Complete)
	require.Contains(t, second.Lines[0].Text, "boundary")
	require.Equal(t, int64(len(content)), second.AfterCursor)
	require.LessOrEqual(t, second.AfterCursor-cursor, int64(jobLogScanBytes))

	// A query found only in the continuation remains discoverable when the first page is empty.
	query := &entity.ListJobLogLinesRequest{Level: "error", Query: "boundary"}
	before, err := listNewerLogLines(file, int64(len(content)), query)
	require.NoError(t, err)
	require.Empty(t, before.Lines)
	require.True(t, before.HasNewer)
	cursor = before.AfterCursor
	query.Cursor = &cursor
	after, err := listNewerLogLines(file, int64(len(content)), query)
	require.NoError(t, err)
	require.Len(t, after.Lines, 1)
	require.Contains(t, after.Lines[0].Text, "boundary")

	// A continuation with no visible line start is contextual even when its match is unknown.
	unknown := &entity.ListJobLogLinesRequest{Level: "warning", Query: "absent", Cursor: &cursor}
	contextPage, err := listNewerLogLines(file, int64(len(content)), unknown)
	require.NoError(t, err)
	require.Len(t, contextPage.Lines, 1)
	require.True(t, contextPage.Lines[0].Continuation)
	require.Empty(t, contextPage.Lines[0].Level)

	// Tail-first reading can cross an empty filtered page before the continuation and line start.
	older := &entity.ListJobLogLinesRequest{Level: "error", Query: "needle"}
	tail, err := listOlderLogLines(file, int64(len(content)), older)
	require.NoError(t, err)
	require.Empty(t, tail.Lines)
	require.True(t, tail.HasOlder)
	require.LessOrEqual(t, tail.AfterCursor-tail.BeforeCursor, int64(jobLogScanBytes))
	cursor = tail.BeforeCursor
	older.Cursor = &cursor
	middle, err := listOlderLogLines(file, int64(len(content)), older)
	require.NoError(t, err)
	require.Len(t, middle.Lines, 1)
	require.True(t, middle.Lines[0].Continuation)
	require.LessOrEqual(t, middle.AfterCursor-middle.BeforeCursor, int64(jobLogScanBytes))
	cursor = middle.BeforeCursor
	older.Cursor = &cursor
	head, err := listOlderLogLines(file, int64(len(content)), older)
	require.NoError(t, err)
	require.Len(t, head.Lines, 1)
	require.Contains(t, head.Lines[0].Text, "needle")
	require.False(t, head.HasOlder)
}
