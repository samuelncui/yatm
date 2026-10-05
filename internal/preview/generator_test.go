package preview

import (
	"context"
	"crypto/sha256"
	"image"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/previewprotocol"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestGenerationUsesLiveRuntimeAndFrozenOutputSettings(t *testing.T) {
	helper, ffmpeg := testPreviewHelper(t), testFFmpeg(t)
	for _, kind := range []string{"image", "video"} {
		t.Run(kind, func(t *testing.T) {
			// Freeze only output choices; a historical helper path must not control the next file.
			root := t.TempDir()
			source := writeImageFixture(t, ffmpeg, root)
			extension, role := "png", "thumbnail"
			options := map[string]any{"format": "png", "max_width": 80, "max_height": 80}
			if kind == "video" {
				source = writeVideoFixture(t, ffmpeg, root, "video.mp4", 2, 10)
				extension, role = "mp4", "poster"
				options = map[string]any{"format": "png", "poster_width": 80, "poster_height": 80,
					"timeline_width": 45, "timeline_height": 81, "timeline_interval_seconds": 1, "timeline_max_frames": 2}
			}
			config := Config{Root: filepath.Join(root, "previews"), Generators: []GeneratorConfig{
				{Kind: kind, Extensions: []string{extension}, Options: options},
			}}
			frozenSettings, err := SettingsFromConfig(config)
			require.NoError(t, err)
			frozenSettings.Enabled, frozenSettings.Command = true, filepath.Join(root, "obsolete-helper")
			frozen := &entity.PreviewJobSettings{Generators: frozenSettings.GetGenerators()}
			original := proto.Clone(frozen)
			// Reproduce the old resolution path without editing shared production source.
			oldConfigs, err := generatorConfigs(frozenSettings)
			require.NoError(t, err)
			oldGenerators, err := loadGenerators(oldConfigs)
			require.NoError(t, err)
			_, err = oldGenerators[extension].generator.Generate(context.Background(), source, t.TempDir())
			require.ErrorContains(t, err, "Preview helper unavailable")
			live := proto.Clone(frozenSettings).(*entity.PreviewSettings)
			live.Command, live.Concurrency = helper, 1
			if kind == "image" {
				live.Generators[0].GetImage().MaxWidth = 200
			} else {
				live.Generators[0].GetVideo().PosterWidth = 200
			}
			manager, err := New(config, "")
			require.NoError(t, err)
			var lock sync.Mutex
			manager.settings = func(context.Context) (*entity.PreviewSettings, error) {
				lock.Lock()
				defer lock.Unlock()
				return live, nil
			}
			data, err := os.ReadFile(source)
			require.NoError(t, err)
			info, err := os.Stat(source)
			require.NoError(t, err)
			hash := sha256.Sum256(data)

			// Hold the sole slot and change the live pixel ceiling only after this file queues.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			held, owner, err := manager.beginGeneration(ctx, "held", source, "held")
			require.NoError(t, err)
			require.True(t, owner)
			queued := make(chan struct{})
			var queuedOnce sync.Once
			waiting := WithProgress(ctx, func(event previewprotocol.Event) {
				if event.Phase == "queued" {
					queuedOnce.Do(func() { close(queued) })
				}
			})
			done := make(chan error, 1)
			go func() {
				_, err := manager.Generate(waiting, source, hash[:], info.Size(), info.ModTime().UnixNano(), true, frozen)
				done <- err
			}()
			select {
			case <-queued:
			case <-ctx.Done():
				t.Fatal("generation never queued")
			}
			lock.Lock()
			live = proto.Clone(live).(*entity.PreviewSettings)
			live.MaxInputPixels = 1
			lock.Unlock()
			manager.finishGeneration("held", held, true, nil)
			select {
			case err := <-done:
				require.ErrorContains(t, err, "Preview helper failed")
				if kind == "video" {
					require.ErrorContains(t, err, "pixel limit")
				}
			case <-ctx.Done():
				t.Fatal("queued generation never finished")
			}

			// Restore the runtime ceiling: the same frozen Job now succeeds with its original geometry.
			lock.Lock()
			live = proto.Clone(live).(*entity.PreviewSettings)
			live.MaxInputPixels = 64_000_000
			lock.Unlock()
			signature, err := manager.Generate(ctx, source, hash[:], info.Size(), info.ModTime().UnixNano(), true, frozen)
			require.NoError(t, err)
			manifest, err := manager.Manifest(signature)
			require.NoError(t, err)
			require.JSONEq(t, string(manager.generators[extension].settingsJSON), string(manifest.SettingsJson))
			actual := decodePreviewAsset(t, manager, signature, role)
			require.Equal(t, 80, actual.Width)
			require.Equal(t, 45, actual.Height)
			if kind == "video" {
				timeline := decodePreviewAsset(t, manager, signature, "timeline")
				require.Equal(t, 90, timeline.Width)
				require.Equal(t, 81, timeline.Height)
			}
			require.True(t, proto.Equal(original, frozen), "generation mutated frozen Job settings")
		})
	}
}

func TestGenerationRequiresFrozenAndLiveRoute(t *testing.T) {
	// A Job can keep only the route it captured, while live Settings may turn that route off.
	root := t.TempDir()
	source := filepath.Join(root, "source.jpg")
	require.NoError(t, os.WriteFile(source, []byte("not decoded"), 0o644))
	info, err := os.Stat(source)
	require.NoError(t, err)
	hash := sha256.Sum256([]byte("not decoded"))
	settings, err := SettingsFromConfig(Config{Generators: []GeneratorConfig{{
		Kind: "image", Extensions: []string{"jpg"}, Options: map[string]any{},
	}}})
	require.NoError(t, err)
	settings.Enabled = true
	frozen := &entity.PreviewJobSettings{Generators: proto.Clone(settings).(*entity.PreviewSettings).Generators}
	live := proto.Clone(settings).(*entity.PreviewSettings)
	live.Generators[0].Extensions = []*entity.PreviewExtension{{Name: "png", Enabled: true}}
	manager, err := NewWithSettings(context.Background(), filepath.Join(root, "previews"), "", func(context.Context) (*entity.PreviewSettings, error) {
		return proto.Clone(live).(*entity.PreviewSettings), nil
	})
	require.NoError(t, err)

	// Live removal blocks the captured route, and live addition cannot expand the Job's route set.
	_, err = manager.Generate(
		context.Background(), source, hash[:], info.Size(), info.ModTime().UnixNano(), true, frozen,
	)
	require.ErrorIs(t, err, ErrDisabled)
	require.False(t, manager.Supports("source.png", frozen), "live Settings cannot add a route to the frozen Job")
}

func TestNativeCapabilitiesExposeRAWByImageKind(t *testing.T) {
	// Probe the packaged executable through the real manager/public capability projection.
	manager, err := New(Config{Root: t.TempDir()}, "")
	require.NoError(t, err)
	helper := testPreviewHelper(t)
	settings, err := SettingsFromConfig(Config{})
	require.NoError(t, err)
	settings.Command = helper
	manager.settings = func(context.Context) (*entity.PreviewSettings, error) { return settings, nil }
	reply, err := manager.Capabilities(context.Background())
	require.NoError(t, err)
	require.True(t, reply.Available, reply.Reason)
	require.NotEmpty(t, reply.LibrawVersion)

	// Extension routing remains image-specific and does not change the master enable preference.
	for _, extension := range []string{"dng", "cr2", "cr3", "nef", "nrw", "arw", "raf", "orf", "rw2", "pef", "srw"} {
		require.Contains(t, reply.InputExtensionsByKind["image"].Extensions, extension)
		require.NotContains(t, reply.InputExtensionsByKind["video"].Extensions, extension)
	}
	require.False(t, settings.Enabled)
}

func TestBuiltInGenerators(t *testing.T) {
	// Exercise every packaged output encoder through one native helper request per source kind.
	helper := testPreviewHelper(t)
	ffmpeg := testFFmpeg(t)
	for _, format := range []string{"png", "jpeg", "webp"} {
		t.Run(format, func(t *testing.T) {
			// Build deterministic input media and isolate each encoded bundle store.
			root := t.TempDir()
			imagePath := writeImageFixture(t, ffmpeg, root)
			videoPath := writeVideoFixture(t, ffmpeg, root, "video.mp4", 2, 10)
			previews, err := New(Config{Root: filepath.Join(root, "previews"), Generators: []GeneratorConfig{
				{Kind: "image", Extensions: []string{"png"}, Options: map[string]any{"command": helper, "format": format}},
				{Kind: "video", Extensions: []string{"mp4"}, Options: map[string]any{
					"command": helper, "format": format, "timeline_width": 45, "timeline_height": 81,
					"timeline_interval_seconds": 1, "timeline_max_frames": 2,
				}},
			}}, "")
			require.NoError(t, err)

			// Generate the image and validate that its manifest resource remains decodable in the stored bundle.
			imageSignature := generateFixturePreview(t, previews, imagePath)
			imageManifest, err := previews.Manifest(imageSignature)
			require.NoError(t, err)
			require.Equal(t, []string{"thumbnail"}, assetRoles(imageManifest.Assets))
			config := decodePreviewAsset(t, previews, imageSignature, "thumbnail")
			require.Equal(t, 320, config.Width)
			require.Equal(t, 180, config.Height)

			// Generate video assets and validate every resource, including the configured sprite geometry.
			videoSignature := generateFixturePreview(t, previews, videoPath)
			videoManifest, err := previews.Manifest(videoSignature)
			require.NoError(t, err)
			require.Equal(t, []string{"poster", "timeline", "timeline-map"}, assetRoles(videoManifest.Assets))
			for _, asset := range videoManifest.Assets {
				if asset.GetMediaType() == "text/vtt" {
					reader, err := previews.Open(videoSignature, asset.GetRole())
					require.NoError(t, err)
					contents, err := io.ReadAll(reader)
					require.NoError(t, err)
					require.NoError(t, reader.Close())
					require.Contains(t, string(contents), "WEBVTT")
					require.Contains(t, string(contents), "#xywh=45,0,45,81")
					continue
				}
				config := decodePreviewAsset(t, previews, videoSignature, asset.GetRole())
				require.Positive(t, config.Width)
				require.Positive(t, config.Height)
				if asset.GetRole() == "timeline" {
					require.Equal(t, 90, config.Width)
					require.Equal(t, 81, config.Height)
				}
			}
		})
	}
}

func TestBuiltInGeneratorsAcceptRelativeStoreAndSourcePaths(t *testing.T) {
	// Share the packaged helper without changing the process-wide working directory.
	helper, ffmpeg := testPreviewHelper(t), testFFmpeg(t)
	workingDirectory, err := os.Getwd()
	require.NoError(t, err)
	for _, kind := range []string{"image", "video"} {
		t.Run(kind, func(t *testing.T) {
			// Legacy deployment paths are relative to the service's working directory.
			root := t.TempDir()
			source := writeImageFixture(t, ffmpeg, root)
			extension, role := "png", "thumbnail"
			if kind == "video" {
				source = writeVideoFixture(t, ffmpeg, root, "video.mp4", 2, 10)
				extension, role = "mp4", "poster"
			}
			relativeSource, err := filepath.Rel(workingDirectory, source)
			require.NoError(t, err)
			relativeStore, err := filepath.Rel(workingDirectory, filepath.Join(root, "previews"))
			require.NoError(t, err)
			require.False(t, filepath.IsAbs(relativeSource))
			require.False(t, filepath.IsAbs(relativeStore))
			previews, err := New(Config{Root: relativeStore, Generators: []GeneratorConfig{
				{Kind: kind, Extensions: []string{extension}, Options: map[string]any{"command": helper, "format": "png"}},
			}}, "")
			require.NoError(t, err)
			signature := generateFixturePreview(t, previews, relativeSource)

			// Publication is readable and only the final content-addressed bundle remains.
			manifest, err := previews.Manifest(signature)
			require.NoError(t, err)
			require.NotEmpty(t, manifest.Assets)
			actual := decodePreviewAsset(t, previews, signature, role)
			require.Equal(t, 320, actual.Width)
			require.Equal(t, 180, actual.Height)
			var bundles int
			require.NoError(t, filepath.WalkDir(previews.root, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				require.NotContains(t, entry.Name(), ".generate-")
				require.NotContains(t, entry.Name(), ".bundle-")
				if !entry.IsDir() {
					bundles++
					require.Equal(t, ".zip", filepath.Ext(path))
				}
				return nil
			}))
			require.Equal(t, 1, bundles)

			// Missing-only reuse must not rewrite an existing relative-root bundle.
			bundle, err := previews.bundlePath(signature)
			require.NoError(t, err)
			before, err := os.Stat(bundle)
			require.NoError(t, err)
			require.Equal(t, signature, generateFixturePreview(t, previews, relativeSource))
			after, err := os.Stat(bundle)
			require.NoError(t, err)
			require.Equal(t, before.ModTime(), after.ModTime(), "existing bundle was regenerated")
		})
	}
}

func assetRoles(assets []*entity.PreviewAsset) []string {
	roles := make([]string, 0, len(assets))
	for _, asset := range assets {
		roles = append(roles, asset.Role)
	}
	return roles
}

func decodePreviewAsset(t *testing.T, previews *Manager, signature []byte, role string) image.Config {
	t.Helper()
	reader, err := previews.Open(signature, role)
	require.NoError(t, err)
	defer reader.Close()
	config, _, err := image.DecodeConfig(reader)
	require.NoError(t, err)
	return config
}

func generateFixturePreview(t *testing.T, previews *Manager, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	hash := sha256.Sum256(data)
	signature, err := previews.Generate(
		context.Background(), path, hash[:], info.Size(), info.ModTime().UnixNano(), false, nil,
	)
	require.NoError(t, err)
	return signature
}

func testPreviewHelper(t *testing.T) string {
	t.Helper()
	helper := os.Getenv("YATM_TEST_PREVIEW_HELPER")
	if helper == "" {
		t.Skip("YATM_TEST_PREVIEW_HELPER is not set; native helper integration is unavailable")
	}
	info, err := os.Stat(helper)
	require.NoError(t, err)
	require.False(t, info.IsDir())
	return helper
}

func testFFmpeg(t *testing.T) string {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		if os.Getenv("YATM_TEST_PREVIEW_HELPER") != "" {
			t.Fatal("ffmpeg fixture generator is required for packaged Preview acceptance")
		}
		t.Skip("ffmpeg fixture generator is unavailable")
	}
	return ffmpeg
}

func writeImageFixture(t *testing.T, ffmpeg, root string) string {
	t.Helper()
	path := filepath.Join(root, "image.png")
	output, err := exec.Command(
		ffmpeg, "-y", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=red:s=320x180", "-frames:v", "1", path,
	).CombinedOutput()
	require.NoError(t, err, string(output))
	return path
}

func writeVideoFixture(t *testing.T, ffmpeg, root, name string, seconds, rate int) string {
	t.Helper()
	path := filepath.Join(root, name)
	output, err := exec.Command(
		ffmpeg,
		"-y", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate="+strconv.Itoa(rate),
		"-t", strconv.Itoa(seconds), "-c:v", "libx264", "-g", strconv.Itoa(rate), "-keyint_min", strconv.Itoa(rate),
		"-sc_threshold", "0", "-pix_fmt", "yuv420p", path,
	).CombinedOutput()
	require.NoError(t, err, string(output))
	return path
}
