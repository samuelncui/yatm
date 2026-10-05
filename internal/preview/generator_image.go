package preview

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/internal/previewprotocol"
)

type imageOptions struct {
	MaxInputPixels int64  `json:"-" yaml:"max_input_pixels"`
	Command        string `json:"-" yaml:"command"`
	MaxWidth       int    `json:"max_width" yaml:"max_width"`
	MaxHeight      int    `json:"max_height" yaml:"max_height"`
	Format         string `json:"format" yaml:"format"`
	Quality        int    `json:"quality" yaml:"quality"`
}

type imageGenerator struct {
	options imageOptions
}

func init() {
	RegisterGenerator("image", newImageGenerator)
}

func newImageGenerator(values map[string]any) (Generator, error) {
	// Decode the configured command and derivative settings over safe defaults.
	options := imageOptions{MaxInputPixels: 64_000_000, MaxWidth: 512, MaxHeight: 512, Format: "webp", Quality: 80}
	if err := decodeOptions(values, &options); err != nil {
		return nil, err
	}

	// Reject settings that could produce invalid or unbounded output.
	if options.MaxWidth <= 0 || options.MaxHeight <= 0 {
		return nil, fmt.Errorf("invalid image Preview dimensions, width=%d height=%d", options.MaxWidth, options.MaxHeight)
	}
	if options.MaxWidth > maxPreviewDimension || options.MaxHeight > maxPreviewDimension {
		return nil, fmt.Errorf(
			"image Preview dimensions exceed limit, width=%d height=%d limit=%d",
			options.MaxWidth,
			options.MaxHeight,
			maxPreviewDimension,
		)
	}
	if options.Quality < 1 || options.Quality > 100 {
		return nil, fmt.Errorf("invalid image Preview quality, quality=%d", options.Quality)
	}

	// Resolve the validated output encoding once for every generated Preview.
	format, err := normalizeOutputFormat(options.Format)
	if err != nil {
		return nil, err
	}
	options.Format = format
	return &imageGenerator{options: options}, nil
}

func (g *imageGenerator) Settings() any {
	return g.options
}

func (g *imageGenerator) Generate(ctx context.Context, sourcePath, outputDir string) ([]*Asset, error) {
	return generateNative(ctx, g.options.Command, previewprotocol.Request{
		Protocol: previewprotocol.Version, Kind: "image", SourcePath: sourcePath, OutputDir: outputDir,
		Format: g.options.Format, Quality: g.options.Quality, MaxInputPixels: g.options.MaxInputPixels,
		MaxWidth: g.options.MaxWidth, MaxHeight: g.options.MaxHeight,
	})
}
