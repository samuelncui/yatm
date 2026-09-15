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

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
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
}

// StorageRoot identifies the actual managed resource for online-source exclusions.
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
	return &Manager{root: root, generators: generators}, nil
}

func (m *Manager) Supports(sourcePath string) bool {
	_, exists := m.generators[sourceExtension(sourcePath)]
	return exists
}

func (m *Manager) Generate(
	ctx context.Context,
	sourcePath string,
	sha256 []byte,
	size int64,
	mtimeNS int64,
	force bool,
) ([]byte, error) {
	// Resolve the generator and verify the ACP content facts before generation.
	configured, exists := m.generators[sourceExtension(sourcePath)]
	if !exists {
		return nil, fmt.Errorf("%w, path=%q", ErrUnsupported, sourcePath)
	}
	before, err := os.Stat(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("stat Preview source failed, path=%q, %w", sourcePath, err)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("Preview source is not a regular file, path=%q", sourcePath)
	}
	if before.Size() != size || before.ModTime().UnixNano() != mtimeNS {
		return nil, fmt.Errorf("Preview source changed after hashing, path=%q", sourcePath)
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
	if _, err := os.Stat(bundlePath); err == nil {
		if !force {
			return signature, nil
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat Preview bundle failed, path=%q, %w", bundlePath, err)
	}

	// Generate bounded derivative files beside the content-addressed store.
	temporaryDir, err := os.MkdirTemp(m.root, ".generate-")
	if err != nil {
		return nil, fmt.Errorf("create Preview temporary directory failed, %w", err)
	}
	defer os.RemoveAll(temporaryDir)
	assets, err := configured.generator.Generate(ctx, sourcePath, temporaryDir)
	if err != nil {
		return nil, fmt.Errorf("generate Preview failed, path=%q kind=%q, %w", sourcePath, configured.kind, err)
	}
	after, err := os.Stat(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("restat Preview source failed, path=%q, %w", sourcePath, err)
	}
	if after.Size() != size || after.ModTime().UnixNano() != mtimeNS {
		return nil, fmt.Errorf("Preview source changed during generation, path=%q", sourcePath)
	}

	// Publish one indexed bundle after every generated asset has been validated.
	manifest, err := buildManifest(signature, configured, temporaryDir, assets)
	if err != nil {
		return nil, err
	}
	if err := publishBundle(bundlePath, temporaryDir, manifest); err != nil {
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
		info, err := os.Stat(filepath.Join(outputDir, asset.Name))
		if err != nil {
			return nil, fmt.Errorf("stat generated Preview asset failed, name=%q, %w", asset.Name, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("generated Preview asset is not a regular file, name=%q", asset.Name)
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
			Name: asset.Name, Role: asset.Role, MediaType: asset.MediaType, Width: asset.Width, Height: asset.Height,
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
