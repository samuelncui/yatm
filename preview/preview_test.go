package preview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/samuelncui/yatm/library"
	"github.com/stretchr/testify/require"
)

type testGenerator struct {
	calls  *int64
	marker string
}

func (g *testGenerator) Settings() any {
	return map[string]any{"marker": g.marker}
}

func (g *testGenerator) Generate(_ context.Context, _ string, outputDir string) ([]*Asset, error) {
	atomic.AddInt64(g.calls, 1)
	if err := os.WriteFile(filepath.Join(outputDir, "thumbnail.webp"), []byte("preview fixture"), 0o644); err != nil {
		return nil, err
	}
	return []*Asset{{Name: "thumbnail.webp", Role: "thumbnail", MediaType: "image/webp"}}, nil
}

var testGeneratorCalls int64

func newTestGenerator(options map[string]any) (Generator, error) {
	marker, _ := options["marker"].(string)
	if marker == "" {
		marker = "configured"
	}
	return &testGenerator{calls: &testGeneratorCalls, marker: marker}, nil
}

func init() {
	RegisterGenerator("test-fixture", newTestGenerator)
	RegisterGenerator("test-fixture-next", newTestGenerator)
}

func TestGenerateStoresAndReusesContentAddressedBundle(t *testing.T) {
	// Configure one deterministic generator and source fixture.
	atomic.StoreInt64(&testGeneratorCalls, 0)
	root := t.TempDir()
	manager, err := New(Config{
		Root: root,
		Generators: []GeneratorConfig{{
			Kind: "test-fixture", Extensions: []string{"fixture"}, Options: map[string]any{"ignored": true},
		}},
	}, "")
	require.NoError(t, err)
	content := []byte("source fixture")
	sourcePath := filepath.Join(t.TempDir(), "source.fixture")
	require.NoError(t, os.WriteFile(sourcePath, content, 0o644))
	require.True(t, manager.Supports(sourcePath))
	require.False(t, manager.Supports("source.txt"))

	// Generate once and verify the exact File Signature fan-out path.
	hash := sha256.Sum256(content)
	info, err := os.Stat(sourcePath)
	require.NoError(t, err)
	signature, err := manager.Generate(
		context.Background(), sourcePath, hash[:], info.Size(), info.ModTime().UnixNano(), false,
	)
	require.NoError(t, err)
	wantSignature, err := library.NewFileSignature(hash[:], int64(len(content)))
	require.NoError(t, err)
	require.Equal(t, wantSignature, signature)
	encoded := hex.EncodeToString(signature)
	bundlePath := filepath.Join(root, encoded[0:2], encoded[2:4], encoded[4:6], encoded[6:]+".zip")
	require.FileExists(t, bundlePath)

	// Read indexed metadata and assets, then confirm a second request reuses the bundle.
	manifest, err := manager.Manifest(signature)
	require.NoError(t, err)
	require.Equal(t, "test-fixture", manifest.Generator)
	require.JSONEq(t, `{"marker":"configured"}`, string(manifest.SettingsJson))
	require.Len(t, manifest.Assets, 1)
	reader, err := manager.Open(signature, "thumbnail")
	require.NoError(t, err)
	asset, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.Equal(t, []byte("preview fixture"), asset)
	_, err = manager.Generate(
		context.Background(), sourcePath, hash[:], info.Size(), info.ModTime().UnixNano(), false,
	)
	require.NoError(t, err)
	require.Equal(t, int64(1), atomic.LoadInt64(&testGeneratorCalls))
}

func TestGenerateForceReplacesExistingBundles(t *testing.T) {
	// Generate one bundle using the initial effective generator settings.
	atomic.StoreInt64(&testGeneratorCalls, 0)
	root := t.TempDir()
	sourcePath := filepath.Join(t.TempDir(), "source.fixture")
	content := []byte("source fixture")
	require.NoError(t, os.WriteFile(sourcePath, content, 0o644))
	info, err := os.Stat(sourcePath)
	require.NoError(t, err)
	hash := sha256.Sum256(content)
	initial, err := New(Config{Root: root, Generators: []GeneratorConfig{{
		Kind: "test-fixture", Extensions: []string{"fixture"}, Options: map[string]any{"marker": "initial"},
	}}}, "")
	require.NoError(t, err)
	signature, err := initial.Generate(
		context.Background(), sourcePath, hash[:], info.Size(), info.ModTime().UnixNano(), false,
	)
	require.NoError(t, err)

	// Missing-only generation keeps an existing bundle even when generator settings differ.
	updated, err := New(Config{Root: root, Generators: []GeneratorConfig{{
		Kind: "test-fixture", Extensions: []string{"fixture"}, Options: map[string]any{"marker": "updated"},
	}}}, "")
	require.NoError(t, err)
	_, err = updated.Generate(
		context.Background(), sourcePath, hash[:], info.Size(), info.ModTime().UnixNano(), false,
	)
	require.NoError(t, err)
	manifest, err := updated.Manifest(signature)
	require.NoError(t, err)
	require.JSONEq(t, `{"marker":"initial"}`, string(manifest.SettingsJson))
	require.Equal(t, int64(1), atomic.LoadInt64(&testGeneratorCalls))

	// Forced generation replaces the bundle on every request, even with unchanged settings.
	_, err = updated.Generate(
		context.Background(), sourcePath, hash[:], info.Size(), info.ModTime().UnixNano(), true,
	)
	require.NoError(t, err)
	manifest, err = updated.Manifest(signature)
	require.NoError(t, err)
	require.JSONEq(t, `{"marker":"updated"}`, string(manifest.SettingsJson))
	require.Equal(t, int64(2), atomic.LoadInt64(&testGeneratorCalls))
	_, err = updated.Generate(
		context.Background(), sourcePath, hash[:], info.Size(), info.ModTime().UnixNano(), true,
	)
	require.NoError(t, err)
	require.Equal(t, int64(3), atomic.LoadInt64(&testGeneratorCalls))

	// Replace the same content bundle when its configured generator changes.
	next, err := New(Config{Root: root, Generators: []GeneratorConfig{{
		Kind: "test-fixture-next", Extensions: []string{"fixture"}, Options: map[string]any{"marker": "updated"},
	}}}, "")
	require.NoError(t, err)
	_, err = next.Generate(
		context.Background(), sourcePath, hash[:], info.Size(), info.ModTime().UnixNano(), true,
	)
	require.NoError(t, err)
	manifest, err = next.Manifest(signature)
	require.NoError(t, err)
	require.Equal(t, "test-fixture-next", manifest.Generator)
	require.Equal(t, int64(4), atomic.LoadInt64(&testGeneratorCalls))

	// A broken derivative must not prevent explicit regeneration from valid source facts.
	bundlePath, err := next.bundlePath(signature)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(bundlePath, []byte("damaged bundle"), 0o644))
	_, err = next.Generate(context.Background(), sourcePath, hash[:], info.Size(), info.ModTime().UnixNano(), true)
	require.NoError(t, err)
	_, err = next.Manifest(signature)
	require.NoError(t, err)
	require.Equal(t, int64(5), atomic.LoadInt64(&testGeneratorCalls))
}

func TestManifestRejectsInvalidSignature(t *testing.T) {
	manager, err := New(Config{Root: t.TempDir()}, "")
	require.NoError(t, err)
	_, err = manager.Manifest([]byte{1})
	require.ErrorContains(t, err, "invalid file signature length")
}

func TestBuildManifestRejectsOversizedAsset(t *testing.T) {
	// Create a sparse generated asset larger than the publication limit.
	outputDir := t.TempDir()
	assetPath := filepath.Join(outputDir, "oversized.webp")
	file, err := os.Create(assetPath)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(maxPreviewAssetSize+1))
	require.NoError(t, file.Close())
	signature, err := library.NewFileSignature(make([]byte, sha256.Size), 0)
	require.NoError(t, err)

	// Reject the asset before the bundle writer can consume its contents.
	_, err = buildManifest(
		signature,
		configuredGenerator{kind: "test-fixture"},
		outputDir,
		[]*Asset{{Name: "oversized.webp", Role: "thumbnail", MediaType: "image/webp"}},
	)
	require.ErrorContains(t, err, "asset exceeds size limit")
}

func TestBuiltinGeneratorsRejectUnboundedSettings(t *testing.T) {
	// Enforce hard limits independently of trusted configuration defaults.
	_, err := newImageGenerator(map[string]any{"max_width": maxPreviewDimension + 1})
	require.ErrorContains(t, err, "dimensions exceed limit")
	_, err = newVideoGenerator(map[string]any{"timeline_max_frames": maxPreviewTimelineFrames + 1})
	require.ErrorContains(t, err, "frame count exceeds limit")
	_, err = newVideoGenerator(map[string]any{
		"timeline_width":      maxPreviewDimension,
		"timeline_height":     maxPreviewDimension,
		"timeline_max_frames": maxPreviewTimelineFrames,
	})
	require.ErrorContains(t, err, "timeline exceeds pixel limit")
}
