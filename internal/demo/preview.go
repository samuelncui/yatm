package demo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	previewpkg "github.com/samuelncui/yatm/preview"
)

const (
	demoImageGeneratorKind = "demo-image"
	demoVideoGeneratorKind = "demo-video"
)

func init() {
	previewpkg.RegisterGenerator(demoImageGeneratorKind, func(map[string]any) (previewpkg.Generator, error) {
		return demoImageGenerator{}, nil
	})
	previewpkg.RegisterGenerator(demoVideoGeneratorKind, func(map[string]any) (previewpkg.Generator, error) {
		return demoVideoGenerator{}, nil
	})
}

func newPreviewManager(root, work string, realVideo bool) (*previewpkg.Manager, error) {
	video := previewpkg.GeneratorConfig{Kind: demoVideoGeneratorKind, Extensions: []string{"mp4"}}
	if realVideo {
		video.Kind = "video"
		video.Options = map[string]any{"format": "png"}
	}
	return previewpkg.New(previewpkg.Config{
		Root: filepath.Join(root, "previews"),
		Generators: []previewpkg.GeneratorConfig{
			{Kind: demoImageGeneratorKind, Extensions: []string{"png"}},
			video,
		},
	}, work)
}

type demoImageGenerator struct{}

func (demoImageGenerator) Generate(ctx context.Context, sourcePath, outputDir string) (_ []*previewpkg.Asset, returnErr error) {
	// Copy the deterministic PNG through a bounded stream so fixture creation does not require ffmpeg.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("generate Demo Preview canceled, %w", err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("open Demo Preview source failed, path=%q, %w", sourcePath, err)
	}
	defer func() {
		if err := source.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close Demo Preview source failed, path=%q, %w", sourcePath, err))
		}
	}()

	// Publish the copied thumbnail through the normal Preview bundle writer.
	name := "thumbnail.png"
	targetPath := filepath.Join(outputDir, name)
	target, err := os.Create(targetPath)
	if err != nil {
		return nil, fmt.Errorf("create Demo Preview asset failed, path=%q, %w", targetPath, err)
	}
	if _, err := io.Copy(target, source); err != nil {
		_ = target.Close()
		return nil, fmt.Errorf("copy Demo Preview asset failed, path=%q, %w", targetPath, err)
	}
	if err := target.Close(); err != nil {
		return nil, fmt.Errorf("close Demo Preview asset failed, path=%q, %w", targetPath, err)
	}
	return []*previewpkg.Asset{{
		Name: name, Role: "thumbnail", MediaType: "image/png", Width: 320, Height: 180,
	}}, nil
}

type demoVideoGenerator struct{}

func (demoVideoGenerator) Generate(ctx context.Context, sourcePath, outputDir string) ([]*previewpkg.Asset, error) {
	// Validate that the source selected by the normal Preview runner is still available.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("generate Demo video Preview canceled, %w", err)
	}
	if _, err := os.Stat(sourcePath); err != nil {
		return nil, fmt.Errorf("stat Demo video Preview source failed, path=%q, %w", sourcePath, err)
	}

	// Build deterministic poster and timeline images without requiring host ffmpeg.
	poster, err := demoPreviewImage()
	if err != nil {
		return nil, err
	}
	timeline, err := demoTimelineImage()
	if err != nil {
		return nil, err
	}

	// Publish the standard video Preview roles consumed by the frontend.
	if err := writeDemoAsset(outputDir, "poster.png", poster); err != nil {
		return nil, err
	}
	if err := writeDemoAsset(outputDir, "timeline.png", timeline); err != nil {
		return nil, err
	}
	if err := writeDemoAsset(outputDir, "timeline.vtt", []byte(demoTimelineVTT)); err != nil {
		return nil, err
	}
	return []*previewpkg.Asset{
		{Name: "poster.png", Role: "poster", MediaType: "image/png", Width: 320, Height: 180},
		{Name: "timeline.png", Role: "timeline", MediaType: "image/png", Width: 640, Height: 90},
		{Name: "timeline.vtt", Role: "timeline-map", MediaType: "text/vtt"},
	}, nil
}

func writeDemoAsset(outputDir, name string, data []byte) error {
	path := filepath.Join(outputDir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write Demo Preview asset failed, path=%q, %w", path, err)
	}
	return nil
}

const demoTimelineVTT = `WEBVTT

00:00:00.000 --> 00:00:00.250
timeline.png#xywh=0,0,160,90

00:00:00.250 --> 00:00:00.500
timeline.png#xywh=160,0,160,90

00:00:00.500 --> 00:00:00.750
timeline.png#xywh=320,0,160,90

00:00:00.750 --> 00:00:01.000
timeline.png#xywh=480,0,160,90
`
