package preview

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/previewprotocol"
	"google.golang.org/protobuf/proto"
)

const (
	manifestName             = "manifest.pb"
	maxManifestSize          = 1 << 20
	maxAssetCount            = 256
	maxPreviewAssetSize      = 128 << 20
	maxPreviewAssetsSize     = 256 << 20
	maxPreviewDimension      = 8192
	maxPreviewTimelineFrames = 1000
	maxPreviewTimelinePixels = 100_000_000
)

var ErrUnsupported = errors.New("Preview source type is unsupported")

// Asset describes one generated file before it is packed into the Preview bundle.
type Asset struct {
	Name      string
	Role      string
	MediaType string
	Width     uint32
	Height    uint32
}

// Manager owns Preview generation, storage addressing, and indexed bundle reads.
type Manager struct {
	root       string
	generators map[string]configuredGenerator
	settings   func(context.Context) (*entity.PreviewSettings, error)
	lock       sync.Mutex
	active     int
	pending    map[string]*generation
	changed    chan struct{}
}

// StorageRoot identifies the actual managed resource for Location exclusions.
func (m *Manager) StorageRoot() string { return m.root }

// New constructs the Preview module from one effective process configuration.
func New(config Config, work string) (*Manager, error) {
	generators, err := loadGenerators(config.Generators)
	if err != nil {
		return nil, err
	}
	root := resolveRoot(config.Root, work)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create Preview root failed, path=%q, %w", root, err)
	}
	return &Manager{root: root, generators: generators, pending: make(map[string]*generation), changed: make(chan struct{})}, nil
}

func (m *Manager) Supports(sourcePath string, settings *entity.PreviewJobSettings) bool {
	// Jobs carry explicit settings; direct internal consumers can use process defaults.
	if settings != nil {
		return supportsRoute(sourcePath, settings.GetGenerators())
	}
	_, exists := m.generators[sourceExtension(sourcePath)]
	return exists
}

func supportsRoute(sourcePath string, generators []*entity.PreviewGeneratorSettings) bool {
	for _, generator := range generators {
		if !generator.GetEnabled() {
			continue
		}
		for _, extension := range generator.GetExtensions() {
			if extension.GetEnabled() &&
				strings.TrimPrefix(strings.ToLower(strings.TrimSpace(extension.GetName())), ".") == sourceExtension(sourcePath) {
				return true
			}
		}
	}
	return false
}

// Exists reports whether one content already has a published bundle. Publication renames a
// complete archive into place, so presence is the whole question a caller planning work has to
// ask: a bundle is complete or absent, and a store fault shows up when its assets are read.
func (m *Manager) Exists(signature []byte) (bool, error) {
	bundlePath, err := m.bundlePath(signature)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(bundlePath)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("stat Preview bundle failed, path=%q, %w", bundlePath, err)
}

// The frozen update policy preserves existing assets until regeneration is explicitly requested.
func (m *Manager) reuse(ctx context.Context, bundlePath string, force bool) (bool, error) {
	if force {
		return false, nil
	}
	if _, err := os.Stat(bundlePath); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat Preview bundle failed, path=%q, %w", bundlePath, err)
	}
	return true, ctx.Err()
}

func (m *Manager) Generate(
	ctx context.Context,
	sourcePath string,
	sha256 []byte,
	size int64,
	_ int64,
	force bool,
	settings *entity.PreviewJobSettings,
) (result []byte, resultErr error) {
	// Resolve the generator for the content facts already produced by the owning operation.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	generators := m.generators
	if settings != nil {
		configs, err := generatorConfigsFor(settings.GetGenerators(), "", 64_000_000)
		if err != nil {
			return nil, err
		}
		generators, err = loadGenerators(configs)
		if err != nil {
			return nil, err
		}
	}
	configured, exists := generators[sourceExtension(sourcePath)]
	if !exists {
		return nil, fmt.Errorf("%w, path=%q", ErrUnsupported, sourcePath)
	}
	if settings != nil && !m.Supports(sourcePath, settings) {
		return nil, ErrDisabled
	}
	// Resolve the content-addressed bundle and apply the Job's explicit update policy.
	signature, err := library.NewFileSignature(sha256, size)
	if err != nil {
		return nil, fmt.Errorf("build Preview signature failed, path=%q, %w", sourcePath, err)
	}
	bundlePath, err := m.bundlePath(signature)
	if err != nil {
		return nil, err
	}

	// A published bundle is the policy's answer, so no lock, setting read or timeout is needed
	// before returning it. Only real generation takes the active-file budget below.
	reused, err := m.reuse(ctx, bundlePath, force)
	if err != nil {
		return nil, err
	}
	if reused {
		return signature, nil
	}

	// Deduplicate content and enforce one process-wide active-file budget, then start its timeout.
	identity := fmt.Sprintf("%t:%s:%s", force, configured.kind, configured.settingsJSON)
	pending, owner, err := m.beginGeneration(ctx, bundlePath, sourcePath, identity)
	if err != nil {
		return nil, err
	}
	if !owner {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return signature, ctx.Err()
	}
	defer func() { m.finishGeneration(bundlePath, pending, true, resultErr) }()
	current, err := m.currentSettings(ctx)
	if err != nil {
		return nil, err
	}

	// Runtime controls apply when a file starts; the Job's output settings remain immutable.
	if m.settings != nil {
		switch generator := configured.generator.(type) {
		case *imageGenerator:
			copy := *generator
			copy.options.Command, copy.options.MaxInputPixels = current.Command, current.MaxInputPixels
			configured.generator = &copy
		case *videoGenerator:
			copy := *generator
			copy.options.Command, copy.options.MaxInputPixels = current.Command, current.MaxInputPixels
			configured.generator = &copy
		}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(current.TimeoutSeconds)*time.Second)
	defer cancel()
	// Generate bounded derivative files beside the content-addressed store.
	temporaryDir, err := os.MkdirTemp(m.root, ".generate-")
	if err != nil {
		return nil, fmt.Errorf("create Preview temporary directory failed, %w", err)
	}
	defer os.RemoveAll(temporaryDir)
	reportProgress(ctx, previewprotocol.Event{Type: "progress", Phase: "generate"})
	assets, err := configured.generator.Generate(ctx, sourcePath, temporaryDir)
	if err != nil {
		return nil, fmt.Errorf("generate Preview failed, path=%q kind=%q, %w", sourcePath, configured.kind, err)
	}
	// Publish one indexed bundle after every generated asset has been validated.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reportProgress(ctx, previewprotocol.Event{Type: "progress", Phase: "publish"})
	manifest, err := buildManifest(signature, configured, temporaryDir, assets)
	if err != nil {
		return nil, err
	}
	if err := publishBundle(ctx, bundlePath, temporaryDir, manifest); err != nil {
		return nil, err
	}
	return signature, nil
}

func (m *Manager) Manifest(signature []byte) (*entity.PreviewManifest, error) {
	bundlePath, err := m.bundlePath(signature)
	if err != nil {
		return nil, err
	}
	archive, err := zip.OpenReader(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("open Preview bundle failed, path=%q, %w", bundlePath, err)
	}
	defer archive.Close()
	return readManifest(archive.File, signature)
}

func (m *Manager) Open(signature []byte, role string) (io.ReadCloser, error) {
	bundlePath, err := m.bundlePath(signature)
	if err != nil {
		return nil, err
	}
	archive, err := zip.OpenReader(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("open Preview bundle failed, path=%q, %w", bundlePath, err)
	}

	// Resolve the public role through the manifest before opening a ZIP entry.
	manifest, err := readManifest(archive.File, signature)
	if err != nil {
		_ = archive.Close()
		return nil, err
	}
	var assetName string
	for _, asset := range manifest.Assets {
		if asset.Role == role {
			assetName = asset.Name
			break
		}
	}
	if assetName == "" {
		_ = archive.Close()
		return nil, fmt.Errorf("Preview asset role is missing, role=%q", role)
	}
	for _, file := range archive.File {
		if file.Name != assetName {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			_ = archive.Close()
			return nil, fmt.Errorf("open Preview asset failed, role=%q, %w", role, err)
		}
		return &assetReader{ReadCloser: reader, archive: archive}, nil
	}

	_ = archive.Close()
	return nil, fmt.Errorf("Preview asset entry is missing, role=%q name=%q", role, assetName)
}

func (m *Manager) bundlePath(signature []byte) (string, error) {
	if err := library.ValidateFileSignature(signature); err != nil {
		return "", err
	}
	encoded := hex.EncodeToString(signature)
	return filepath.Join(m.root, encoded[0:2], encoded[2:4], encoded[4:6], encoded[6:]+".zip"), nil
}

func sourceExtension(sourcePath string) string {
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(sourcePath)), ".")
}

func buildManifest(
	signature []byte,
	configured configuredGenerator,
	outputDir string,
	assets []*Asset,
) (*entity.PreviewManifest, error) {
	if len(assets) == 0 {
		return nil, fmt.Errorf("Preview generator returned no assets, kind=%q", configured.kind)
	}
	if len(assets) > maxAssetCount {
		return nil, fmt.Errorf("Preview generator returned too many assets, kind=%q count=%d", configured.kind, len(assets))
	}

	// Validate role and entry uniqueness before the bundle becomes visible.
	roles := make(map[string]struct{}, len(assets))
	names := make(map[string]struct{}, len(assets))
	manifestAssets := make([]*entity.PreviewAsset, 0, len(assets))
	var assetsSize int64
	for _, asset := range assets {
		if asset == nil {
			return nil, fmt.Errorf("Preview generator returned a nil asset, kind=%q", configured.kind)
		}
		if asset.Name == "" || strings.ContainsAny(asset.Name, `/\`) || filepath.Base(asset.Name) != asset.Name {
			return nil, fmt.Errorf("invalid Preview asset name, name=%q", asset.Name)
		}
		if asset.Role == "" || asset.MediaType == "" {
			return nil, fmt.Errorf("incomplete Preview asset, name=%q", asset.Name)
		}
		if _, exists := names[asset.Name]; exists {
			return nil, fmt.Errorf("duplicate Preview asset name, name=%q", asset.Name)
		}
		if _, exists := roles[asset.Role]; exists {
			return nil, fmt.Errorf("duplicate Preview asset role, role=%q", asset.Role)
		}
		info, err := os.Lstat(filepath.Join(outputDir, asset.Name))
		if err != nil {
			return nil, fmt.Errorf("stat generated Preview asset failed, name=%q, %w", asset.Name, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("generated Preview asset is not a regular file, name=%q", asset.Name)
		}
		if asset.Width > maxPreviewTimelinePixels || asset.Height > maxPreviewTimelinePixels ||
			uint64(asset.Width)*uint64(asset.Height) > maxPreviewTimelinePixels {
			return nil, fmt.Errorf("generated Preview dimensions exceed limit, name=%q", asset.Name)
		}
		if info.Size() > maxPreviewAssetSize {
			return nil, fmt.Errorf(
				"generated Preview asset exceeds size limit, name=%q size=%d limit=%d",
				asset.Name,
				info.Size(),
				maxPreviewAssetSize,
			)
		}
		if info.Size() > maxPreviewAssetsSize-assetsSize {
			return nil, fmt.Errorf("generated Preview assets exceed total size limit, limit=%d", maxPreviewAssetsSize)
		}
		assetsSize += info.Size()

		names[asset.Name] = struct{}{}
		roles[asset.Role] = struct{}{}
		manifestAssets = append(manifestAssets, &entity.PreviewAsset{
			Name: asset.Name, Role: asset.Role, MediaType: asset.MediaType, WidthPx: asset.Width, HeightPx: asset.Height,
		})
	}

	return &entity.PreviewManifest{
		FileSignature: append([]byte(nil), signature...),
		Generator:     configured.kind,
		SettingsJson:  append([]byte(nil), configured.settingsJSON...),
		Assets:        manifestAssets,
	}, nil
}

func readManifest(files []*zip.File, signature []byte) (*entity.PreviewManifest, error) {
	for _, file := range files {
		if file.Name != manifestName {
			continue
		}
		if file.UncompressedSize64 > maxManifestSize {
			return nil, fmt.Errorf("Preview manifest is too large, size=%d", file.UncompressedSize64)
		}
		reader, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("open Preview manifest failed, %w", err)
		}
		data, readErr := io.ReadAll(io.LimitReader(reader, maxManifestSize+1))
		closeErr := reader.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read Preview manifest failed, %w", readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close Preview manifest failed, %w", closeErr)
		}
		if len(data) > maxManifestSize {
			return nil, fmt.Errorf("Preview manifest exceeds size limit")
		}
		manifest := new(entity.PreviewManifest)
		if err := proto.Unmarshal(data, manifest); err != nil {
			return nil, fmt.Errorf("decode Preview manifest failed, %w", err)
		}
		if !bytes.Equal(manifest.FileSignature, signature) {
			return nil, fmt.Errorf("Preview manifest signature mismatch")
		}
		return manifest, nil
	}
	return nil, fmt.Errorf("Preview manifest entry is missing")
}

type assetReader struct {
	io.ReadCloser
	archive *zip.ReadCloser
}

func (r *assetReader) Close() error {
	return errors.Join(r.ReadCloser.Close(), r.archive.Close())
}
