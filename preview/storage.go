package preview

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
)

func publishBundle(bundlePath, outputDir string, manifest *entity.PreviewManifest) (returnErr error) {
	// Create the complete ZIP beside its final path so publication is one filesystem operation.
	if err := os.MkdirAll(filepath.Dir(bundlePath), 0o755); err != nil {
		return fmt.Errorf("create Preview bundle directory failed, %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(bundlePath), ".bundle-*.zip")
	if err != nil {
		return fmt.Errorf("create Preview bundle failed, %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		if returnErr != nil {
			_ = temporary.Close()
		}
		_ = os.Remove(temporaryPath)
	}()

	archive := zip.NewWriter(temporary)
	manifestData, err := proto.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode Preview manifest failed, %w", err)
	}
	if len(manifestData) > maxManifestSize {
		return fmt.Errorf("Preview manifest exceeds size limit, size=%d", len(manifestData))
	}
	manifestWriter, err := archive.CreateHeader(&zip.FileHeader{Name: manifestName, Method: zip.Deflate})
	if err != nil {
		return fmt.Errorf("create Preview manifest entry failed, %w", err)
	}
	if _, err := manifestWriter.Write(manifestData); err != nil {
		return fmt.Errorf("write Preview manifest entry failed, %w", err)
	}

	// Stream each generated asset into its indexed ZIP entry.
	for _, asset := range manifest.Assets {
		method := uint16(zip.Deflate)
		if strings.HasPrefix(strings.ToLower(asset.MediaType), "image/") {
			method = zip.Store
		}
		entry, err := archive.CreateHeader(&zip.FileHeader{Name: asset.Name, Method: method})
		if err != nil {
			return fmt.Errorf("create Preview asset entry failed, name=%q, %w", asset.Name, err)
		}
		file, err := os.Open(filepath.Join(outputDir, asset.Name))
		if err != nil {
			return fmt.Errorf("open Preview asset failed, name=%q, %w", asset.Name, err)
		}
		_, copyErr := io.Copy(entry, file)
		closeErr := file.Close()
		if copyErr != nil {
			return fmt.Errorf("write Preview asset entry failed, name=%q, %w", asset.Name, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close Preview asset failed, name=%q, %w", asset.Name, closeErr)
		}
	}

	// Finish the central directory and publish only the complete bundle.
	if err := archive.Close(); err != nil {
		return fmt.Errorf("finish Preview bundle failed, %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close Preview bundle failed, %w", err)
	}
	if err := os.Rename(temporaryPath, bundlePath); err != nil {
		return fmt.Errorf("publish Preview bundle failed, path=%q, %w", bundlePath, err)
	}
	return nil
}
