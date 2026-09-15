package preview

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type videoOptions struct {
	FFmpeg                 string `json:"ffmpeg" yaml:"ffmpeg"`
	FFprobe                string `json:"ffprobe" yaml:"ffprobe"`
	PosterWidth            int    `json:"poster_width" yaml:"poster_width"`
	PosterHeight           int    `json:"poster_height" yaml:"poster_height"`
	TimelineWidth          int    `json:"timeline_width" yaml:"timeline_width"`
	TimelineHeight         int    `json:"timeline_height" yaml:"timeline_height"`
	TimelineIntervalSecond int    `json:"timeline_interval_seconds" yaml:"timeline_interval_seconds"`
	TimelineMaxFrames      int    `json:"timeline_max_frames" yaml:"timeline_max_frames"`
	Format                 string `json:"format" yaml:"format"`
	Quality                int    `json:"quality" yaml:"quality"`
}

type videoGenerator struct {
	options videoOptions
	format  outputFormat
}

func init() {
	RegisterGenerator("video", newVideoGenerator)
}

func newVideoGenerator(values map[string]any) (Generator, error) {
	// Decode the configured commands and derivative settings over safe defaults.
	options := videoOptions{
		FFmpeg: "ffmpeg", FFprobe: "ffprobe", PosterWidth: 512, PosterHeight: 512,
		TimelineWidth: 160, TimelineHeight: 90, TimelineIntervalSecond: 10,
		TimelineMaxFrames: 100, Format: "webp", Quality: 75,
	}
	if err := decodeOptions(values, &options); err != nil {
		return nil, err
	}

	// Reject settings that could produce invalid or unbounded output.
	if options.FFmpeg == "" || options.FFprobe == "" {
		return nil, fmt.Errorf("video Preview command is empty")
	}
	if options.PosterWidth <= 0 || options.PosterHeight <= 0 || options.TimelineWidth <= 0 || options.TimelineHeight <= 0 {
		return nil, fmt.Errorf("invalid video Preview dimensions")
	}
	if options.PosterWidth > maxPreviewDimension || options.PosterHeight > maxPreviewDimension ||
		options.TimelineWidth > maxPreviewDimension || options.TimelineHeight > maxPreviewDimension {
		return nil, fmt.Errorf("video Preview dimensions exceed limit, limit=%d", maxPreviewDimension)
	}
	if options.TimelineIntervalSecond <= 0 || options.TimelineMaxFrames <= 0 {
		return nil, fmt.Errorf("invalid video Preview timeline sampling")
	}
	if options.TimelineMaxFrames > maxPreviewTimelineFrames {
		return nil, fmt.Errorf(
			"video Preview frame count exceeds limit, frames=%d limit=%d",
			options.TimelineMaxFrames,
			maxPreviewTimelineFrames,
		)
	}
	columns := options.TimelineMaxFrames
	if columns > 10 {
		columns = 10
	}
	rows := (options.TimelineMaxFrames + columns - 1) / columns
	pixels := int64(columns) * int64(rows) * int64(options.TimelineWidth) * int64(options.TimelineHeight)
	if pixels > maxPreviewTimelinePixels {
		return nil, fmt.Errorf(
			"video Preview timeline exceeds pixel limit, pixels=%d limit=%d",
			pixels,
			maxPreviewTimelinePixels,
		)
	}
	if options.Quality < 1 || options.Quality > 100 {
		return nil, fmt.Errorf("invalid video Preview quality, quality=%d", options.Quality)
	}

	// Resolve the validated output encoding once for every generated Preview.
	format, err := resolveOutputFormat(options.Format, options.Quality)
	if err != nil {
		return nil, err
	}
	return &videoGenerator{options: options, format: format}, nil
}

func (g *videoGenerator) Settings() any {
	return g.options
}

func (g *videoGenerator) Generate(ctx context.Context, sourcePath, outputDir string) ([]*Asset, error) {
	// Probe duration once to build both sampling and WebVTT coordinates.
	output, err := runCommand(
		ctx,
		g.options.FFprobe,
		"-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", sourcePath,
	)
	if err != nil {
		return nil, err
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(output), 64)
	if err != nil || duration <= 0 {
		return nil, fmt.Errorf("invalid video duration, value=%q", strings.TrimSpace(output))
	}

	// Render a poster while preserving the source aspect ratio.
	posterName := "poster." + g.format.extension
	posterPath := filepath.Join(outputDir, posterName)
	posterFilter := fmt.Sprintf(
		"scale='min(%d,iw)':'min(%d,ih)':force_original_aspect_ratio=decrease",
		g.options.PosterWidth,
		g.options.PosterHeight,
	)
	posterArguments := []string{
		"-y", "-loglevel", "error", "-i", sourcePath,
		"-frames:v", "1", "-an", "-sn", "-vf", posterFilter,
	}
	posterArguments = append(posterArguments, g.format.arguments...)
	posterArguments = append(posterArguments, "-fs", strconv.FormatInt(maxPreviewAssetSize, 10))
	posterArguments = append(posterArguments, posterPath)
	if _, err := runCommand(
		ctx,
		g.options.FFmpeg,
		posterArguments...,
	); err != nil {
		return nil, err
	}

	// Sample at most the configured frame count and tile every frame into one sprite.
	interval := math.Max(float64(g.options.TimelineIntervalSecond), duration/float64(g.options.TimelineMaxFrames))
	frameCount := int(math.Ceil(duration / interval))
	if frameCount > g.options.TimelineMaxFrames {
		frameCount = g.options.TimelineMaxFrames
	}
	if frameCount < 1 {
		frameCount = 1
	}
	columns := frameCount
	if columns > 10 {
		columns = 10
	}
	rows := (frameCount + columns - 1) / columns
	timelineName := "timeline." + g.format.extension
	timelinePath := filepath.Join(outputDir, timelineName)
	// RGB padding preserves odd tile dimensions regardless of source chroma subsampling.
	timelineFilter := fmt.Sprintf(
		"select='eq(n\\,0)+gte(t-prev_selected_t\\,%f)',scale=%d:%d:force_original_aspect_ratio=decrease,format=rgb24,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,tile=%dx%d",
		interval,
		g.options.TimelineWidth,
		g.options.TimelineHeight,
		g.options.TimelineWidth,
		g.options.TimelineHeight,
		columns,
		rows,
	)
	timelineArguments := []string{
		"-y", "-loglevel", "error", "-i", sourcePath,
		"-frames:v", "1", "-an", "-sn", "-vf", timelineFilter,
	}
	timelineArguments = append(timelineArguments, g.format.arguments...)
	timelineArguments = append(timelineArguments, "-fs", strconv.FormatInt(maxPreviewAssetSize, 10))
	timelineArguments = append(timelineArguments, timelinePath)
	if _, err := runCommand(
		ctx,
		g.options.FFmpeg,
		timelineArguments...,
	); err != nil {
		return nil, err
	}

	// Map playback time ranges to their sprite coordinates.
	var vtt strings.Builder
	vtt.WriteString("WEBVTT\n\n")
	for index := 0; index < frameCount; index++ {
		start := math.Min(float64(index)*interval, duration)
		end := math.Min(float64(index+1)*interval, duration)
		fmt.Fprintf(
			&vtt,
			"%s --> %s\n%s#xywh=%d,%d,%d,%d\n\n",
			formatVTTTime(start),
			formatVTTTime(end),
			timelineName,
			(index%columns)*g.options.TimelineWidth,
			(index/columns)*g.options.TimelineHeight,
			g.options.TimelineWidth,
			g.options.TimelineHeight,
		)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "timeline.vtt"), []byte(vtt.String()), 0o644); err != nil {
		return nil, fmt.Errorf("write Preview timeline failed, %w", err)
	}

	return []*Asset{
		{Name: posterName, Role: "poster", MediaType: g.format.mediaType},
		{
			Name: timelineName, Role: "timeline", MediaType: g.format.mediaType,
			Width: uint32(columns * g.options.TimelineWidth), Height: uint32(rows * g.options.TimelineHeight),
		},
		{Name: "timeline.vtt", Role: "timeline-map", MediaType: "text/vtt"},
	}, nil
}

func formatVTTTime(seconds float64) string {
	totalMilliseconds := int64(math.Round(seconds * 1000))
	hours := totalMilliseconds / 3_600_000
	minutes := totalMilliseconds / 60_000 % 60
	wholeSeconds := totalMilliseconds / 1_000 % 60
	milliseconds := totalMilliseconds % 1_000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", hours, minutes, wholeSeconds, milliseconds)
}
