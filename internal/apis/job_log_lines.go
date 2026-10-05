package apis

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/samuelncui/yatm/entity"
)

const (
	jobLogScanBytes = 1 << 20
	jobLogReadChunk = 64 << 10
	jobLogPageLines = 200
)

type logLineSpan struct {
	line *entity.JobLogLine
	end  int64
}

func (api *API) ListLogLines(ctx context.Context, req *entity.ListJobLogLinesRequest) (*entity.ListJobLogLinesResponse, error) {
	if req.Cursor != nil && *req.Cursor < 0 {
		return nil, fmt.Errorf("log cursor must not be negative, cursor=%d", *req.Cursor)
	}
	if req.Direction != entity.JobLogDirection_JOB_LOG_DIRECTION_OLDER && req.Direction != entity.JobLogDirection_JOB_LOG_DIRECTION_NEWER {
		return nil, fmt.Errorf("invalid log direction, direction=%s", req.Direction)
	}
	if req.Level != "" && !validLogLevel(req.Level) {
		return nil, fmt.Errorf("invalid log level, level=%q", req.Level)
	}

	// Read one bounded region from the retained Job log. A missing log is an empty page.
	reader, err := api.exe.NewLogReader(ctx, req.Id)
	if err != nil {
		return nil, fmt.Errorf("open log failed, %w", err)
	}
	if reader == nil {
		return &entity.ListJobLogLinesResponse{}, nil
	}
	defer reader.Close()
	info, err := reader.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat log failed, %w", err)
	}

	if req.Direction == entity.JobLogDirection_JOB_LOG_DIRECTION_OLDER {
		return listOlderLogLines(reader, info.Size(), req)
	}
	return listNewerLogLines(reader, info.Size(), req)
}

func listOlderLogLines(reader *os.File, size int64, req *entity.ListJobLogLinesRequest) (*entity.ListJobLogLinesResponse, error) {
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

		// Prefer whole lines, leaving the leading incomplete line for the next older page.
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

func listNewerLogLines(reader *os.File, size int64, req *entity.ListJobLogLinesRequest) (*entity.ListJobLogLinesResponse, error) {
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

		// Stop on a line boundary when the next page can take the remaining bytes.
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

func olderLogBoundary(start int64, data []byte) (int64, []byte) {
	if start == 0 {
		return start, data
	}
	if newline := bytes.IndexByte(data, '\n'); newline >= 0 && newline < len(data)-1 {
		return start + int64(newline+1), data[newline+1:]
	}
	for len(data) > 0 && !utf8.RuneStart(data[0]) {
		start++
		data = data[1:]
	}
	return start, data
}

func newerLogBoundary(start, end, size int64, data []byte) (int64, []byte) {
	if end == size {
		return end, data
	}
	if newline := bytes.LastIndexByte(data, '\n'); newline >= 0 {
		return start + int64(newline+1), data[:newline+1]
	}
	for trim := 0; trim < utf8.UTFMax && len(data) > 0 && !utf8.Valid(data); trim++ {
		end--
		data = data[:len(data)-1]
	}
	return end, data
}

func readLogRegion(reader *os.File, start, end int64) ([]byte, error) {
	data := make([]byte, end-start)
	if _, err := reader.ReadAt(data, start); err != nil && err != io.EOF {
		return nil, fmt.Errorf("read log region failed, offset=%d, %w", start, err)
	}
	return data, nil
}

func splitLogLines(reader *os.File, start int64, data []byte) ([]logLineSpan, error) {
	if len(data) == 0 {
		return nil, nil
	}
	continuation := false
	if start > 0 {
		previous := []byte{0}
		if _, err := reader.ReadAt(previous, start-1); err != nil {
			return nil, fmt.Errorf("read log line boundary failed, offset=%d, %w", start-1, err)
		}
		continuation = previous[0] != '\n'
	}

	spans := make([]logLineSpan, 0, bytes.Count(data, []byte{'\n'})+1)
	for len(data) > 0 {
		length := bytes.IndexByte(data, '\n')
		complete := length >= 0
		if !complete {
			length = len(data)
		}
		consumed := length
		if complete {
			consumed++
		}
		end := start + int64(consumed)
		content := strings.ToValidUTF8(string(data[:length]), "�")
		spans = append(spans, logLineSpan{line: &entity.JobLogLine{
			Offset: start, EndOffset: end, Text: content, Level: logLevel(content), Continuation: continuation, Complete: complete,
		}, end: end})
		start = end
		data = data[consumed:]
		continuation = false
	}
	return spans, nil
}

func logLevel(line string) string {
	for _, field := range strings.Fields(line) {
		if level, ok := strings.CutPrefix(field, "level="); ok {
			level = strings.Trim(level, "\"")
			if validLogLevel(level) {
				return level
			}
		}
	}
	return ""
}

func validLogLevel(level string) bool {
	switch level {
	case "trace", "debug", "info", "warning", "error", "fatal", "panic":
		return true
	}
	return false
}

func matchingLogLines(spans []logLineSpan, req *entity.ListJobLogLinesRequest) []logLineSpan {
	query := strings.ToLower(req.Query)
	matched := make([]logLineSpan, 0, min(len(spans), jobLogPageLines))
	for _, span := range spans {
		// A byte cursor can begin inside a line whose level or earlier query text is outside this
		// bounded read. Retain that continuation as context rather than silently truncating a
		// filtered line. Continuation tells the caller its filter match is not guaranteed.
		if span.line.Continuation {
			matched = append(matched, span)
			continue
		}
		if req.Level != "" && span.line.Level != req.Level {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(span.line.Text), query) {
			continue
		}
		matched = append(matched, span)
	}
	return matched
}

func logPage(spans []logLineSpan, start, end, size int64) *entity.ListJobLogLinesResponse {
	lines := make([]*entity.JobLogLine, 0, len(spans))
	for _, span := range spans {
		lines = append(lines, span.line)
	}
	return &entity.ListJobLogLinesResponse{
		Lines: lines, BeforeCursor: start, AfterCursor: end, HasOlder: start > 0, HasNewer: end < size,
	}
}
