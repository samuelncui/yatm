package preview

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
)

func publishBundle(
	ctx context.Context, bundlePath, outputDir string, manifest *entity.PreviewManifest,
) (returnErr error) {
	// Create the complete ZIP beside its final path so publication is one filesystem operation.
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(bundlePath), 0o755); err != nil {
		return fmt.Errorf("create Preview bundle directory failed, %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(bundlePath), ".bundle-*.zip")
	if err != nil {
		return fmt.Errorf("create Preview bundle failed, %w", err)
	}
	temporaryPath := temporary.Name()
	open, published := true, false
	defer func() {
		if open {
			if err := temporary.Close(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close Preview bundle failed, %w", err))
			}
		}
		if !published {
			if err := os.Remove(temporaryPath); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("remove temporary Preview bundle failed, %w", err))
			}
		}
	}()

	// Closing the archive also returns compression resources after an incomplete asset.
	archive := zip.NewWriter(temporary)
	archiveOpen := true
	defer func() {
		if archiveOpen {
			if err := archive.Close(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("finish Preview bundle failed, %w", err))
			}
		}
	}()
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
		if err := writeBundleAsset(ctx, archive, outputDir, asset); err != nil {
			return err
		}
	}

	// Finish the central directory and publish only the complete bundle.
	archiveOpen = false
	if err := archive.Close(); err != nil {
		return fmt.Errorf("finish Preview bundle failed, %w", err)
	}
	open = false
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close Preview bundle failed, %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, bundlePath); err != nil {
		return fmt.Errorf("publish Preview bundle failed, path=%q, %w", bundlePath, err)
	}
	published = true
	return nil
}

func writeBundleAsset(
	ctx context.Context, archive *zip.Writer, outputDir string, asset *entity.PreviewAsset,
) (returnErr error) {
	// Keep each input handle local to one entry rather than retaining a bundle's entire asset set.
	file, err := os.Open(filepath.Join(outputDir, asset.Name))
	if err != nil {
		return fmt.Errorf("open Preview asset failed, name=%q, %w", asset.Name, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close Preview asset failed, name=%q, %w", asset.Name, err))
		}
	}()

	// Store already compressed images and stream other assets through bounded compression.
	method := uint16(zip.Deflate)
	if strings.HasPrefix(strings.ToLower(asset.MediaType), "image/") {
		method = zip.Store
	}
	entry, err := archive.CreateHeader(&zip.FileHeader{Name: asset.Name, Method: method})
	if err != nil {
		return fmt.Errorf("create Preview asset entry failed, name=%q, %w", asset.Name, err)
	}
	if _, err := io.Copy(entry, &contextReader{ctx: ctx, reader: file}); err != nil {
		return fmt.Errorf("write Preview asset entry failed, name=%q, %w", asset.Name, err)
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}
