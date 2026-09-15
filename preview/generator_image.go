package preview

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
)

type imageOptions struct {
	Command   string `json:"command" yaml:"command"`
	MaxWidth  int    `json:"max_width" yaml:"max_width"`
	MaxHeight int    `json:"max_height" yaml:"max_height"`
	Format    string `json:"format" yaml:"format"`
	Quality   int    `json:"quality" yaml:"quality"`
}

type imageGenerator struct {
	options imageOptions
	format  outputFormat
}

func init() {
	RegisterGenerator("image", newImageGenerator)
}

func newImageGenerator(values map[string]any) (Generator, error) {
	// Decode the configured command and derivative settings over safe defaults.
	options := imageOptions{Command: "ffmpeg", MaxWidth: 512, MaxHeight: 512, Format: "webp", Quality: 80}
	if err := decodeOptions(values, &options); err != nil {
		return nil, err
	}

	// Reject settings that could produce invalid or unbounded output.
	if options.Command == "" {
		return nil, fmt.Errorf("image Preview command is empty")
	}
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
	format, err := resolveOutputFormat(options.Format, options.Quality)
	if err != nil {
		return nil, err
	}
	return &imageGenerator{options: options, format: format}, nil
}

func (g *imageGenerator) Settings() any {
	return g.options
}

func (g *imageGenerator) Generate(ctx context.Context, sourcePath, outputDir string) ([]*Asset, error) {
	// Render one bounded thumbnail through the configured ffmpeg executable.
	name := "thumbnail." + g.format.extension
	outputPath := filepath.Join(outputDir, name)
	filter := fmt.Sprintf(
		"scale='min(%d,iw)':'min(%d,ih)':force_original_aspect_ratio=decrease",
		g.options.MaxWidth,
		g.options.MaxHeight,
	)
	arguments := []string{
		"-y", "-loglevel", "error", "-i", sourcePath,
		"-frames:v", "1", "-an", "-sn", "-vf", filter,
	}
	arguments = append(arguments, g.format.arguments...)
	arguments = append(arguments, "-fs", strconv.FormatInt(maxPreviewAssetSize, 10))
	arguments = append(arguments, outputPath)
	if _, err := runCommand(
		ctx,
		g.options.Command,
		arguments...,
	); err != nil {
		return nil, err
	}
	return []*Asset{{Name: name, Role: "thumbnail", MediaType: g.format.mediaType}}, nil
}
