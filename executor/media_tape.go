package executor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor/jobstorage"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
	"github.com/samuelncui/yatm/tools"
	"gorm.io/gorm"
)

type tapeReadSession struct {
	backend         *mediaBackend
	media           *library.Media
	capabilities    mediapkg.Capabilities
	device          string
	mountPoint      string
	tapeDir         string
	recycleKey      func()
	indexPath       string
	inventoryDigest []byte
}

func (b *mediaBackend) newTapeReadSession(
	ctx context.Context,
	db *gorm.DB,
	target *entity.ReadTapeTarget,
	expectation *entity.ReadMediaTarget,
) (mediapkg.ReadSession, error) {
	// Reserve the configured drive and resolve the loaded Tape identity.
	device := strings.TrimSpace(target.GetDevice())
	if device == "" {
		return nil, fmt.Errorf("restore Tape device is empty")
	}
	if !b.executor.LeaseTapeDevice(b.jobID, device) {
		return nil, fmt.Errorf("restore Tape device is busy, device=%q", device)
	}
	barcode, err := b.executor.ReadTapeBarcode(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("read restore Tape identity failed, %w", err)
	}
	if barcode == "" {
		return nil, fmt.Errorf("restore Tape identity is unavailable")
	}
	stored, err := b.executor.Lib().GetMediaByIdentity(ctx, entity.MediaKind_MEDIA_KIND_TAPE, barcode)
	if err != nil {
		return nil, err
	}
	if stored == nil {
		return nil, fmt.Errorf("restore Tape is not in Library, barcode=%q", barcode)
	}
	if err := validateReadExpectation(stored, expectation); err != nil {
		return nil, err
	}

	// Validate the durable storage profile without depending on any runner's manifest schema.
	capabilities, err := mediapkg.CapabilitiesForProfile(stored.Profile)
	if err != nil {
		return nil, err
	}
	profile := stored.Profile.GetTape()
	if profile == nil {
		return nil, fmt.Errorf("restore Tape profile is invalid, barcode=%q", barcode)
	}

	// Configure encryption and mount the Tape before exposing a read Session.
	tapeDir, err := b.executor.EnsureTapeWorkPath(ctx, b.jobID, barcode)
	if err != nil {
		return nil, err
	}
	// A read inventory must originate in this mount, never a stale schema left in the Job workspace.
	indexPath := filepath.Join(tapeDir, barcode+".schema")
	if err := os.Remove(indexPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale read index failed, %w", err)
	}
	keyPath, recycleKey, err := b.executor.RestoreKey(profile.Encryption)
	if err != nil {
		return nil, fmt.Errorf("restore Tape encryption key failed, %w", err)
	}
	if err := b.configureTape(ctx, device, keyPath, barcode, stored.Name, tapeDir); err != nil {
		recycleKey()
		return nil, err
	}
	mountPoint, err := b.mountTape(ctx, device, tapeDir)
	if err != nil {
		recycleKey()
		return nil, err
	}
	return &tapeReadSession{
		backend: b, media: stored, capabilities: capabilities, mountPoint: mountPoint,
		device: device, tapeDir: tapeDir, recycleKey: recycleKey, indexPath: indexPath,
	}, nil
}

func (s *tapeReadSession) Capabilities() mediapkg.Capabilities { return s.capabilities }

func (s *tapeReadSession) Inspect() *library.Media { return s.media }

func (s *tapeReadSession) SourcePath(relative string) (string, error) {
	return mediapkg.ResolveSourcePath(s.mountPoint, relative)
}

func (s *tapeReadSession) Finalize(ctx context.Context) error {
	// A successful read observation requires the same cartridge through physical finalization.
	defer s.recycleKey()
	barcode, identityErr := s.backend.executor.ReadTapeBarcode(ctx, s.device)
	if identityErr == nil && barcode != s.media.Identity {
		identityErr = fmt.Errorf("Tape identity changed during read, expected=%q actual=%q", s.media.Identity, barcode)
	}

	// Always release LTFS resources, even when identity validation invalidates all observed results.
	var snapshotErr error
	if len(s.inventoryDigest) > 0 {
		if err := os.Remove(s.indexPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			snapshotErr = fmt.Errorf("discard mount-time Tape inventory failed: %w", err)
		}
	}
	unmountErr := s.backend.unmountTape(ctx, s.device, s.mountPoint, s.tapeDir)
	var indexErr error
	if len(s.inventoryDigest) > 0 && unmountErr == nil && snapshotErr == nil {
		_, indexErr = s.readInventory(ctx, nil)
	}
	return errors.Join(identityErr, snapshotErr, unmountErr, indexErr)
}

func (s *tapeReadSession) WalkInventory(ctx context.Context, yield func(*mediapkg.InventoryEntry) error) error {
	// The mount-time LTFS index supplies authoritative paths and physical extents; live stat supplies read facts.
	_, err := s.readInventory(ctx, func(entry *mediapkg.LTFSIndexEntry) error {
		filename, err := s.SourcePath(entry.Path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(filename)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() != entry.Size {
			return fmt.Errorf("Tape file conflicts with fresh LTFS index: %q", entry.Path)
		}
		return yield(&mediapkg.InventoryEntry{Path: entry.Path, Info: info, Storage: entry.Storage})
	})
	return err
}

func (s *tapeReadSession) readInventory(ctx context.Context, yield func(*mediapkg.LTFSIndexEntry) error) ([]byte, error) {
	// Hash only canonical inventory metadata, not file content, to compare the final unmount snapshot.
	if err := waitForTapeIndex(ctx, s.indexPath); err != nil {
		return nil, err
	}
	file, err := os.Open(s.indexPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	digest := sha256.New()
	encoder := json.NewEncoder(digest)
	if err := mediapkg.ParseLTFSIndex(ctx, file, func(entry *mediapkg.LTFSIndexEntry) error {
		if err := encoder.Encode(entry); err != nil {
			return err
		}
		if yield != nil {
			return yield(entry)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	actual := digest.Sum(nil)
	if len(s.inventoryDigest) > 0 && string(actual) != string(s.inventoryDigest) {
		return nil, fmt.Errorf("Tape inventory changed during read")
	}
	s.inventoryDigest = actual
	return actual, nil
}

type tapeWriteSession struct {
	backend      *mediaBackend
	db           *gorm.DB
	media        *library.Media
	capabilities mediapkg.Capabilities
	barcode      string
	device       string
	mountPoint   string
	tapeDir      string
	indexPath    string
	pathPrefix   string
	recycleKey   func()
}

func (b *mediaBackend) newTapeWriteSession(
	ctx context.Context,
	db *gorm.DB,
	target *entity.ArchiveTapeTarget,
) (mediapkg.WriteSession, error) {
	// Reserve the requested drive and reject a different loaded Tape before any mutation.
	device := strings.TrimSpace(target.GetDevice())
	if device == "" {
		return nil, fmt.Errorf("archive Tape device is empty")
	}
	barcode, err := NormalizeTapeBarcode(target.GetBarcode())
	if err != nil {
		return nil, err
	}
	if !b.executor.LeaseTapeDevice(b.jobID, device) {
		return nil, fmt.Errorf("archive Tape device is busy, device=%q", device)
	}
	deviceBarcode, err := b.executor.ReadTapeBarcode(ctx, device)
	if err != nil {
		return nil, err
	}
	if deviceBarcode == "" {
		return nil, fmt.Errorf("archive Tape identity is unavailable")
	}
	if deviceBarcode != barcode {
		return nil, fmt.Errorf("archive Tape changed, requested=%q device=%q", barcode, deviceBarcode)
	}
	var pending int64
	if err := db.WithContext(ctx).Model(&jobstorage.ArchiveItem{}).
		Where("status = ?", entity.CopyStatus_PENDING).Count(&pending).Error; err != nil {
		return nil, fmt.Errorf("count pending Archive items failed, %w", err)
	}
	if pending == 0 {
		return nil, fmt.Errorf("Archive manifest has no pending items")
	}

	// Resolve the requested FORMAT or APPEND Media and its encryption key.
	stored, err := b.executor.Lib().GetMediaByIdentity(ctx, entity.MediaKind_MEDIA_KIND_TAPE, barcode)
	if err != nil {
		return nil, err
	}
	pendingMedia, keyPath, recycleKey, format, err := b.prepareTapeMedia(target, stored, barcode)
	if err != nil {
		return nil, err
	}
	tapeDir, err := b.executor.EnsureTapeWorkPath(ctx, b.jobID, barcode)
	if err != nil {
		recycleKey()
		return nil, err
	}
	indexPath := filepath.Join(tapeDir, barcode+".schema")
	if err := os.Remove(indexPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		recycleKey()
		return nil, fmt.Errorf("remove stale LTFS index failed, barcode=%q, %w", barcode, err)
	}

	// Configure, optionally format, and mount the physical Tape.
	if err := b.configureTape(ctx, device, keyPath, barcode, pendingMedia.Name, tapeDir); err != nil {
		recycleKey()
		return nil, err
	}
	if format {
		if err := b.formatTape(ctx, device, barcode, pendingMedia.Name, tapeDir); err != nil {
			recycleKey()
			return nil, err
		}
	}
	mountPoint, err := b.mountTape(ctx, device, tapeDir)
	if err != nil {
		recycleKey()
		return nil, err
	}
	// Discard the mount-time snapshot so only the post-unmount Index can satisfy finalization.
	if err := os.Remove(indexPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		unmountErr := b.unmountTape(tools.WithoutTimeout(ctx), device, mountPoint, tapeDir)
		recycleKey()
		return nil, errors.Join(
			fmt.Errorf("remove mounted LTFS index failed, barcode=%q, %w", barcode, err),
			unmountErr,
		)
	}
	pathPrefix := "."
	if !format {
		pathPrefix, err = allocateMediaPathPrefix(mountPoint, uint64(time.Now().UnixNano()))
		if err != nil {
			unmountErr := b.unmountTape(tools.WithoutTimeout(ctx), device, mountPoint, tapeDir)
			recycleKey()
			return nil, errors.Join(err, unmountErr)
		}
	}
	return &tapeWriteSession{
		backend: b, db: db, media: pendingMedia,
		capabilities: mediapkg.Capabilities{Read: mediapkg.AccessSequential, Write: mediapkg.AccessSequential},
		barcode:      barcode, device: device, mountPoint: mountPoint, tapeDir: tapeDir, indexPath: indexPath,
		pathPrefix: pathPrefix, recycleKey: recycleKey,
	}, nil
}

func (b *mediaBackend) prepareTapeMedia(
	target *entity.ArchiveTapeTarget,
	stored *library.Media,
	barcode string,
) (*library.Media, string, func(), bool, error) {
	switch target.GetMode() {
	case entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT:
		if stored != nil {
			return nil, "", nil, false, fmt.Errorf(
				"format Tape already exists in Library; delete it first, barcode=%q", barcode,
			)
		}
		encryption, keyPath, recycleKey, err := b.executor.NewKey()
		if err != nil {
			return nil, "", nil, false, fmt.Errorf("create Tape encryption key failed, %w", err)
		}
		return &library.Media{
			Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: barcode, Name: strings.TrimSpace(target.GetName()),
			Profile: (&entity.TapeMediaProfile{
				Encryption: encryption, Format: library.TapeFormatLTFSV1,
			}).Pack(),
			CreateTime: time.Now(),
		}, keyPath, recycleKey, true, nil
	case entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND:
		if stored == nil {
			return nil, "", nil, false, fmt.Errorf("append Tape is not in Library, barcode=%q", barcode)
		}
		profile := stored.Profile.GetTape()
		if profile == nil || profile.Format != library.TapeFormatLTFSV1 || stored.DestroyTime != nil {
			return nil, "", nil, false, fmt.Errorf("append Tape is unavailable, barcode=%q", barcode)
		}
		keyPath, recycleKey, err := b.executor.RestoreKey(profile.Encryption)
		if err != nil {
			return nil, "", nil, false, fmt.Errorf("restore append Tape key failed, barcode=%q, %w", barcode, err)
		}
		return stored, keyPath, recycleKey, false, nil
	default:
		return nil, "", nil, false, fmt.Errorf("unsupported Tape write mode, mode=%s", target.GetMode())
	}
}

func (s *tapeWriteSession) Capabilities() mediapkg.Capabilities { return s.capabilities }

func (s *tapeWriteSession) Inspect() *library.Media { return s.media }

func (s *tapeWriteSession) TargetPath(relative string) (string, error) {
	if err := entity.ValidateRelativePath(relative); err != nil {
		return "", err
	}
	mediaPath := filepath.ToSlash(filepath.Join(s.pathPrefix, relative))
	return mediapkg.ResolveTargetPath(s.mountPoint, mediaPath)
}

func (s *tapeWriteSession) Finalize(ctx context.Context, copyErr error) error {
	// Establish the physical durability boundary before consulting any captured Index.
	defer s.recycleKey()
	noSpaceErr := targetNoSpaceError(copyErr)
	if err := s.backend.unmountTape(ctx, s.device, s.mountPoint, s.tapeDir); err != nil {
		resetErr := resetArchiveStaged(ctx, s.db)
		return errors.Join(mediapkg.ErrFinalizeUnusable, err, resetErr, noSpaceErr)
	}

	// Normalize staged paths only after a successful normal unmount.
	if err := prefixArchiveStagedPaths(ctx, s.db, s.pathPrefix); err != nil {
		resetErr := resetArchiveStaged(ctx, s.db)
		return errors.Join(mediapkg.ErrFinalizeUnusable, err, resetErr, noSpaceErr)
	}

	// Publish candidates only when the post-unmount Index provides usable physical facts.
	indexErr := s.reconcileIndex(ctx, noSpaceErr != nil)
	if errors.Is(indexErr, errUnusableTapeResult) {
		resetErr := resetArchiveStaged(ctx, s.db)
		return errors.Join(mediapkg.ErrFinalizeUnusable, indexErr, resetErr, noSpaceErr)
	}
	return errors.Join(indexErr, noSpaceErr)
}

var (
	errInvalidTapeIndex   = errors.New("invalid final Tape index")
	errUnusableTapeResult = errors.New("unusable Tape finalize result")
)

func (s *tapeWriteSession) reconcileIndex(ctx context.Context, verifiedPrefix bool) error {
	if err := waitForTapeIndex(ctx, s.indexPath); err != nil {
		return errors.Join(
			errInvalidTapeIndex, errUnusableTapeResult,
			fmt.Errorf("wait for final LTFS index failed, barcode=%q, %w", s.barcode, err),
		)
	}
	file, err := os.Open(s.indexPath)
	if err != nil {
		return errors.Join(
			errInvalidTapeIndex, errUnusableTapeResult,
			fmt.Errorf("open LTFS index failed, barcode=%q, %w", s.barcode, err),
		)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return errors.Join(
			errInvalidTapeIndex, errUnusableTapeResult,
			fmt.Errorf("stat LTFS index failed, barcode=%q, %w", s.barcode, err),
		)
	}
	if info.Size() == 0 {
		return errors.Join(
			errInvalidTapeIndex, errUnusableTapeResult,
			fmt.Errorf("LTFS index is empty, barcode=%q", s.barcode),
		)
	}

	var validationErr error
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := mediapkg.ParseLTFSIndex(ctx, file, func(entry *mediapkg.LTFSIndexEntry) error {
			return attachTapeStorage(tx, entry)
		}); err != nil {
			return errors.Join(errInvalidTapeIndex, errUnusableTapeResult, err)
		}
		if verifiedPrefix {
			_, validationErr = keepVerifiedArchivePrefix(tx)
			return validationErr
		}
		missing, err := resetUnverifiedArchiveItems(tx)
		if err != nil {
			return err
		}
		if missing > 0 {
			validationErr = fmt.Errorf("LTFS index is missing staged files, count=%d", missing)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errUnusableTapeResult) {
			return err
		}
		return errors.Join(
			errUnusableTapeResult,
			fmt.Errorf("reconcile archive Tape failed, barcode=%q, %w", s.barcode, err),
		)
	}
	return validationErr
}

func waitForTapeIndex(ctx context.Context, filename string) error {
	timer := time.NewTimer(2 * time.Minute)
	defer timer.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		info, err := os.Stat(filename)
		if err == nil {
			if info.Size() > 0 {
				return nil
			}
			lastErr = fmt.Errorf("LTFS index is empty, path=%q", filename)
		} else {
			lastErr = err
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), lastErr)
		case <-timer.C:
			return lastErr
		case <-ticker.C:
		}
	}
}

func attachTapeStorage(tx *gorm.DB, entry *mediapkg.LTFSIndexEntry) error {
	var items []*jobstorage.ArchiveItem
	result := tx.Select("id", "result").Where(
		"status = ? AND media_path = ?", entity.CopyStatus_STAGED, entry.Path,
	).Limit(2).Find(&items)
	if result.Error != nil {
		return fmt.Errorf("query staged Archive item failed, path=%q, %w", entry.Path, result.Error)
	}
	if len(items) == 0 {
		return nil
	}
	if len(items) != 1 || items[0].Result == nil {
		return errors.Join(
			errInvalidTapeIndex, errUnusableTapeResult,
			fmt.Errorf("invalid staged Archive item, path=%q", entry.Path),
		)
	}
	item := items[0]
	if item.Result.Storage != nil {
		return errors.Join(
			errInvalidTapeIndex, errUnusableTapeResult,
			fmt.Errorf("LTFS index contains duplicate path, path=%q", entry.Path),
		)
	}
	if item.Result.Size != entry.Size {
		return nil
	}
	item.Result.Storage = entry.Storage
	updated := tx.Model(&jobstorage.ArchiveItem{}).Where(
		"id = ? AND status = ?", item.ID, entity.CopyStatus_STAGED,
	).Update("result", item.Result)
	if updated.Error != nil || updated.RowsAffected != 1 {
		return fmt.Errorf(
			"store LTFS position failed, path=%q affected=%d, %w", entry.Path, updated.RowsAffected, updated.Error,
		)
	}
	return nil
}

func resetUnverifiedArchiveItems(tx *gorm.DB) (int64, error) {
	var cursor string
	var reset int64
	for {
		var items []*jobstorage.ArchiveItem
		if err := tx.Where("status = ? AND target_path > ?", entity.CopyStatus_STAGED, cursor).
			Order("target_path").Limit(256).Find(&items).Error; err != nil {
			return 0, fmt.Errorf("query staged Archive verification failed, cursor=%q, %w", cursor, err)
		}
		if len(items) == 0 {
			return reset, nil
		}
		for _, item := range items {
			cursor = item.TargetPath
			if item.Result != nil && item.Result.Storage != nil {
				continue
			}
			if err := resetOneArchiveItem(tx, item.ID); err != nil {
				return 0, err
			}
			reset++
		}
	}
}

func keepVerifiedArchivePrefix(tx *gorm.DB) (int64, error) {
	var cursor string
	var kept int64
	closed := false
	for {
		var items []*jobstorage.ArchiveItem
		if err := tx.Where("status IN ? AND target_path > ?", []entity.CopyStatus{
			entity.CopyStatus_PENDING, entity.CopyStatus_STAGED,
		}, cursor).Order("target_path").Limit(256).Find(&items).Error; err != nil {
			return 0, fmt.Errorf("query Archive verified prefix failed, cursor=%q, %w", cursor, err)
		}
		if len(items) == 0 {
			return kept, nil
		}
		for _, item := range items {
			cursor = item.TargetPath
			verified := item.Status == entity.CopyStatus_STAGED && item.Result != nil && item.Result.Storage != nil
			if !closed && verified {
				kept++
				continue
			}
			closed = true
			if item.Status == entity.CopyStatus_STAGED {
				if err := resetOneArchiveItem(tx, item.ID); err != nil {
					return 0, err
				}
			}
		}
	}
}

func (b *mediaBackend) configureTape(ctx context.Context, device, keyPath, barcode, name, tapeDir string) error {
	cmd := b.executor.MakeEncryptCmd(ctx, device, keyPath, barcode, name)
	cmd.Env = append(cmd.Env, fmt.Sprintf("TAPE_DIR=%s", tapeDir))
	if err := tools.RunCmd(b.logger, cmd); err != nil {
		return fmt.Errorf("configure Tape encryption failed, %w", err)
	}
	return nil
}

func (b *mediaBackend) formatTape(ctx context.Context, device, barcode, name, tapeDir string) error {
	cmd := exec.CommandContext(ctx, b.executor.Scripts().Mkfs)
	cmd.Env = append(cmd.Env,
		fmt.Sprintf("DEVICE=%s", device), fmt.Sprintf("TAPE_BARCODE=%s", barcode),
		fmt.Sprintf("TAPE_NAME=%s", name), fmt.Sprintf("TAPE_DIR=%s", tapeDir),
	)
	if err := tools.RunCmd(b.logger, cmd); err != nil {
		return fmt.Errorf("format Tape failed, %w", err)
	}
	return nil
}

func (b *mediaBackend) mountTape(ctx context.Context, device, tapeDir string) (string, error) {
	mountPoint, err := os.MkdirTemp("", "yatm-ltfs-*")
	if err != nil {
		return "", fmt.Errorf("create Tape mount point failed, %w", err)
	}
	cmd := exec.CommandContext(ctx, b.executor.Scripts().Mount)
	cmd.Env = append(cmd.Env,
		fmt.Sprintf("DEVICE=%s", device), fmt.Sprintf("MOUNT_POINT=%s", mountPoint),
		fmt.Sprintf("TAPE_DIR=%s", tapeDir),
	)
	if err := tools.RunCmd(b.logger, cmd); err != nil {
		_ = os.Remove(mountPoint)
		return "", fmt.Errorf("mount Tape failed, %w", err)
	}
	return mountPoint, nil
}

func (b *mediaBackend) unmountTape(ctx context.Context, device, mountPoint, tapeDir string) error {
	// Run the complete normal-unmount and physical-device release boundary.
	cmd := exec.CommandContext(ctx, b.executor.Scripts().Umount)
	cmd.Env = append(cmd.Env,
		fmt.Sprintf("DEVICE=%s", device), fmt.Sprintf("MOUNT_POINT=%s", mountPoint),
		fmt.Sprintf("TAPE_DIR=%s", tapeDir),
	)
	if err := tools.RunCmd(b.logger, cmd); err != nil {
		b.executor.keepTapeDeviceUnavailable(b.jobID, device)
		return fmt.Errorf("unmount Tape failed, mount_point=%q, %w", mountPoint, err)
	}

	// Remove only the now-unmounted temporary directory; failure is diagnostic, not physical uncertainty.
	if err := os.Remove(mountPoint); err != nil {
		b.logger.WithContext(ctx).WithError(err).Warnf("remove Tape mount point failed, mount_point=%q", mountPoint)
	}
	return nil
}
