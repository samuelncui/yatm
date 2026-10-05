package preview

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/internal/previewprotocol"
)

type videoOptions struct {
	Command                string `json:"-" yaml:"command"`
	MaxInputPixels         int64  `json:"-" yaml:"max_input_pixels"`
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
}

func init() {
	RegisterGenerator("video", newVideoGenerator)
}

func newVideoGenerator(values map[string]any) (Generator, error) {
	// Decode the configured commands and derivative settings over safe defaults.
	options := videoOptions{
		MaxInputPixels: 64_000_000, PosterWidth: 512, PosterHeight: 512,
		TimelineWidth: 320, TimelineHeight: 180, TimelineIntervalSecond: 10,
		TimelineMaxFrames: 30, Format: "webp", Quality: 75,
	}
	if err := decodeOptions(values, &options); err != nil {
		return nil, err
	}

	// Reject settings that could produce invalid or unbounded output.
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
	format, err := normalizeOutputFormat(options.Format)
	if err != nil {
		return nil, err
	}
	options.Format = format
	return &videoGenerator{options: options}, nil
}

func (g *videoGenerator) Settings() any {
	return g.options
}

func (g *videoGenerator) Generate(ctx context.Context, sourcePath, outputDir string) ([]*Asset, error) {
	return generateNative(ctx, g.options.Command, previewprotocol.Request{
		Protocol: previewprotocol.Version, Kind: "video", SourcePath: sourcePath, OutputDir: outputDir,
		Format: g.options.Format, Quality: g.options.Quality, MaxInputPixels: g.options.MaxInputPixels,
		PosterWidth: g.options.PosterWidth, PosterHeight: g.options.PosterHeight,
		TimelineWidth: g.options.TimelineWidth, TimelineHeight: g.options.TimelineHeight,
		TimelineIntervalSeconds: g.options.TimelineIntervalSecond, TimelineMaxFrames: g.options.TimelineMaxFrames,
	})
}
