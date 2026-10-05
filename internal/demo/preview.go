package demo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/samuelncui/yatm/entity"
	previewpkg "github.com/samuelncui/yatm/internal/preview"
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

func newPreviewManager(root, work string, videoOverride bool) (*demoPreviewManager, error) {
	// Bundled derivatives keep the default fixture independent of an installed native worker.
	video := previewpkg.GeneratorConfig{Kind: demoVideoGeneratorKind, Extensions: []string{"mp4"}}
	if videoOverride {
		video.Kind = "video"
		video.Options = map[string]any{"format": "png"}
	}

	// Both routes publish their assets through the normal Preview manager.
	manager, err := previewpkg.New(previewpkg.Config{
		Root: filepath.Join(root, "previews"),
		Generators: []previewpkg.GeneratorConfig{
			{Kind: demoImageGeneratorKind, Extensions: []string{"png"}},
			video,
		},
	}, work)
	if err != nil {
		return nil, err
	}
	return &demoPreviewManager{Manager: manager, native: videoOverride}, nil
}

type demoPreviewManager struct {
	*previewpkg.Manager
	native bool
}

func (m *demoPreviewManager) CheckGeneration(ctx context.Context) error {
	if m.native {
		return m.Manager.CheckGeneration(ctx)
	}
	return nil
}

func (m *demoPreviewManager) Capabilities(ctx context.Context) (*entity.GetPreviewCapabilitiesResponse, error) {
	// An explicit override uses the actual worker's discovery result.
	if m.native {
		return m.Manager.Capabilities(ctx)
	}

	// Default setup advertises only the routes supplied by the bundled fixture.
	return &entity.GetPreviewCapabilitiesResponse{
		Available: true, Version: "bundled-demo-assets",
		Kinds: []string{"image", "video"}, InputExtensions: []string{"png", "mp4"}, OutputFormats: []string{"png"},
		InputExtensionsByKind: map[string]*entity.PreviewInputFormats{
			"image": {Extensions: []string{"png"}}, "video": {Extensions: []string{"mp4"}},
		},
	}, nil
}

func (m *demoPreviewManager) Generate(
	ctx context.Context,
	sourcePath string,
	sha256 []byte,
	size, mtimeNS int64,
	force bool,
	_ *entity.PreviewJobSettings,
) ([]byte, error) {
	// Publish bundled assets, or generate the supplied video, through normal Preview routing.
	return m.Manager.Generate(ctx, sourcePath, sha256, size, mtimeNS, force, nil)
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
	// Bind the native derivatives to the exact excerpt, including copies at other physical paths.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("generate Demo video Preview canceled, %w", err)
	}
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("read Demo video Preview source failed, path=%q, %w", sourcePath, err)
	}
	if !bytes.Equal(source, bundledVideo) {
		return nil, fmt.Errorf("bundled Demo video Preview requires the bundled excerpt, path=%q", sourcePath)
	}

	// Carry the actual native output through the standard Preview bundle writer.
	if err := writeDemoAsset(outputDir, "poster.png", bundledVideoPoster); err != nil {
		return nil, err
	}
	if err := writeDemoAsset(outputDir, "timeline.png", bundledVideoTimeline); err != nil {
		return nil, err
	}
	if err := writeDemoAsset(outputDir, "timeline.vtt", bundledVideoTimelineMap); err != nil {
		return nil, err
	}
	return []*previewpkg.Asset{
		{Name: "poster.png", Role: "poster", MediaType: "image/png", Width: 512, Height: 288},
		{Name: "timeline.png", Role: "timeline", MediaType: "image/png", Width: 3200, Height: 540},
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
