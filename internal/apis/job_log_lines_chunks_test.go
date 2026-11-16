package apis

import (
	"math/rand"
	"os"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// referenceOlderLogLines re-splits the whole accumulated region after every chunk; the paged
// reader splits each chunk once and must return the same page.
func referenceOlderLogLines(reader *os.File, size int64, req *entity.ListJobLogLinesRequest) (*entity.ListJobLogLinesResponse, error) {
	end := size
	if req.Cursor != nil && *req.Cursor < end {
		end = *req.Cursor
	}
	start := end
	var data []byte
	var matched []logLineSpan
	for start > 0 && end-start < jobLogScanBytes {
		chunkStart := max(int64(0), start-jobLogReadChunk, end-jobLogScanBytes)
		chunk, err := readLogRegion(reader, chunkStart, start)
		if err != nil {
			return nil, err
		}
		data = append(chunk, data...)
		start = chunkStart
		pageStart, pageData := olderLogBoundary(start, data)
		spans, err := splitLogLines(reader, pageStart, pageData)
		if err != nil {
			return nil, err
		}
		matched = matchingLogLines(spans, req)
		if len(matched) >= jobLogPageLines || start == 0 || end-start == jobLogScanBytes {
			start = pageStart
			break
		}
	}
	if len(matched) > jobLogPageLines {
		matched = matched[len(matched)-jobLogPageLines:]
		start = matched[0].line.Offset
	}
	return logPage(matched, start, end, size), nil
}

func referenceNewerLogLines(reader *os.File, size int64, req *entity.ListJobLogLinesRequest) (*entity.ListJobLogLinesResponse, error) {
	start := int64(0)
	if req.Cursor != nil {
		start = min(*req.Cursor, size)
	}
	end := start
	var data []byte
	var matched []logLineSpan
	for end < size && end-start < jobLogScanBytes {
		chunkEnd := min(size, end+jobLogReadChunk, start+jobLogScanBytes)
		chunk, err := readLogRegion(reader, end, chunkEnd)
		if err != nil {
			return nil, err
		}
		data = append(data, chunk...)
		end = chunkEnd
		pageEnd, pageData := newerLogBoundary(start, end, size, data)
		spans, err := splitLogLines(reader, start, pageData)
		if err != nil {
			return nil, err
		}
		matched = matchingLogLines(spans, req)
		if len(matched) >= jobLogPageLines || end == size || end-start == jobLogScanBytes {
			end = pageEnd
			break
		}
	}
	if len(matched) > jobLogPageLines {
		matched = matched[:jobLogPageLines]
		end = matched[len(matched)-1].end
	}
	return logPage(matched, start, end, size), nil
}

func TestJobLogLinesChunkedSplitMatchesWholeRegionSplit(t *testing.T) {
	// Lines cross 64 KiB chunk boundaries, end exactly on them, exceed whole chunks and carry
	// multibyte runes; cursors land inside lines, on newlines and on chunk boundaries.
	random := rand.New(rand.NewSource(1))
	var content strings.Builder
	levels := []string{"info", "error", "warning"}
	for content.Len() < 3*jobLogScanBytes/2 {
		var length int
		switch random.Intn(10) {
		case 0:
			length = jobLogReadChunk + random.Intn(2*jobLogReadChunk)
		case 1:
			length = jobLogReadChunk - content.Len()%jobLogReadChunk - 1
		default:
			length = random.Intn(400)
		}
		line := "level=" + levels[random.Intn(len(levels))] + " msg=" + strings.Repeat("é", max(0, length)/2)
		if random.Intn(5) == 0 {
			line += " needle"
		}
		content.WriteString(line + "\n")
	}
	// A line longer than the scan bound leaves both directions without any whole line.
	huge := int64(content.Len())
	content.WriteString("level=info msg=" + strings.Repeat("é", jobLogScanBytes*5/8) + "\n")
	content.WriteString("level=error msg=unterminated é")
	file := testLogFile(t, content.String())
	size := int64(content.Len())

	cursors := []int64{0, 1, size - 1, size, jobLogReadChunk, jobLogReadChunk - 1, jobLogReadChunk + 1, jobLogScanBytes,
		huge + 1, huge + jobLogScanBytes/2 + 1, size - 2*jobLogReadChunk}
	for range 8 {
		cursors = append(cursors, random.Int63n(size+1))
	}
	for offset, value := range []byte(content.String()) {
		if value == '\n' && random.Intn(100) == 0 && len(cursors) < 40 {
			cursors = append(cursors, int64(offset), int64(offset+1))
		}
	}
	filters := []*entity.ListJobLogLinesRequest{{}, {Level: "error"}, {Level: "warning", Query: "needle"}}
	for _, cursor := range cursors {
		for _, filter := range filters {
			req := proto.Clone(filter).(*entity.ListJobLogLinesRequest)
			req.Cursor = proto.Int64(cursor)
			want, err := referenceOlderLogLines(file, size, req)
			require.NoError(t, err)
			got, err := listOlderLogLines(file, size, req)
			require.NoError(t, err)
			require.True(t, proto.Equal(want, got), "older cursor=%d filter=%v", cursor, filter)
			want, err = referenceNewerLogLines(file, size, req)
			require.NoError(t, err)
			got, err = listNewerLogLines(file, size, req)
			require.NoError(t, err)
			require.True(t, proto.Equal(want, got), "newer cursor=%d filter=%v", cursor, filter)
		}
	}
}
