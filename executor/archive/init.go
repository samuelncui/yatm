package archive

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"gorm.io/gorm"
)

func (a *jobArchiveRunner) applySpec(ctx context.Context, spec *entity.ArchiveJobSpec) error {
	// Library selections freeze expected identities instead of indexing raw filesystem paths.
	if spec != nil && (len(spec.FileIds) != 0 || len(spec.Selections) != 0) {
		return a.applyLibrarySpec(ctx, spec)
	}

	// Preserve the existing raw-source manifest behavior.
	if spec == nil || len(spec.Sources) == 0 {
		return fmt.Errorf("archive sources are empty")
	}
	release, err := a.exe.Lib().UseOnlineRead()
	if err != nil {
		return err
	}
	defer release()
	if err := a.resetManifest(ctx); err != nil {
		return err
	}

	progress := a.getProgress()
	batch := make([]*Item, 0, batchSize)
	var totalFiles, totalBytes int64
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := a.db.WithContext(ctx).CreateInBatches(batch, batchSize).Error; err != nil {
			return fmt.Errorf("create archive manifest failed, %w", err)
		}
		progress.SetGlobalTotal(totalBytes, totalFiles)
		batch = batch[:0]
		return nil
	}
	visit := func(item *Item) error {
		if err := a.captureRawInput(ctx, item); err != nil {
			return err
		}
		totalFiles++
		totalBytes += item.Size
		batch = append(batch, item)
		if len(batch) < batchSize {
			return nil
		}
		return flush()
	}

	for _, source := range spec.Sources {
		if err := a.walkSource(ctx, source, visit); err != nil {
			return err
		}
	}
	if err := flush(); err != nil {
		return err
	}
	if totalFiles == 0 {
		return fmt.Errorf("archive sources contain no regular files")
	}
	return nil
}

func (a *jobArchiveRunner) resetManifest(ctx context.Context) error {
	if err := a.db.WithContext(ctx).
		Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&Item{}).Error; err != nil {
		return fmt.Errorf("reset archive manifest failed, %w", err)
	}
	a.dropProgress()
	return nil
}

func (a *jobArchiveRunner) walkSource(
	ctx context.Context,
	source *entity.Source,
	visit func(*Item) error,
) error {
	if source == nil {
		return fmt.Errorf("archive source is nil")
	}

	sourcePath := path.Join(source.Path...)
	mediaRoot := path.Clean(strings.TrimPrefix(sourcePath, "/"))
	return a.exe.WalkSource(ctx, source, func(root, filename string, info os.FileInfo) error {
		resolvedMediaRoot := mediaRoot
		if resolvedMediaRoot == "." {
			resolvedMediaRoot = filepath.ToSlash(filepath.Base(root))
		}
		return a.visitArchiveFile(root, filename, resolvedMediaRoot, info, visit)
	})
}

func (a *jobArchiveRunner) visitArchiveFile(
	root, sourcePath, mediaRoot string,
	info os.FileInfo,
	visit func(*Item) error,
) error {
	if info.Name() == ".DS_Store" || info.Mode()&acp.UnexpectFileMode != 0 || !info.Mode().IsRegular() {
		return nil
	}
	relative, err := filepath.Rel(root, sourcePath)
	if err != nil {
		return fmt.Errorf("resolve archive source path failed, path=%q, %w", sourcePath, err)
	}
	mediaPath := mediaRoot
	if relative != "." {
		mediaPath = path.Join(mediaRoot, filepath.ToSlash(relative))
	}
	if err := entity.ValidateRelativePath(mediaPath); err != nil {
		return fmt.Errorf("invalid Archive Media path, %w", err)
	}
	data := &entity.ArchiveManifestFile{SourcePath: filepath.Clean(sourcePath)}
	if err := data.Validate(); err != nil {
		return err
	}
	return visit(&Item{
		Status:     entity.CopyStatus_PENDING,
		Size:       info.Size(),
		TargetPath: mediaPath,
		Data:       data,
	})
}

// captureRawInput also prepares unfinished legacy manifests, which did not carry frozen content identities.
func (a *jobArchiveRunner) captureRawInput(ctx context.Context, item *Item) error {
	// Allocate before writing; a retry reuses the Job's already checkpointed identity.
	var input RawInput
	if err := a.db.WithContext(ctx).Where("target_path = ?", item.TargetPath).Limit(1).Find(&input).Error; err != nil {
		return err
	}
	if input.FileID == 0 {
		file, err := a.exe.Lib().CreateArchiveFile(ctx, item.TargetPath)
		if err != nil {
			return err
		}
		input = RawInput{TargetPath: item.TargetPath, FileID: file.ID}
		if err := a.db.WithContext(ctx).Create(&input).Error; err != nil {
			return err
		}
	}
	info, err := os.Lstat(item.Data.SourcePath)
	if err != nil {
		return err
	}
	expected, err := executor.HashObservedContent(ctx, item.Data.SourcePath, info)
	if err != nil {
		return err
	}
	expected.FileId = input.FileID
	if expected.Size != item.Size {
		return fmt.Errorf("raw Archive input size changed; reindex the Job")
	}
	item.Data.Expected = expected

	return nil
}
