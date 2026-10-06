package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/tools"
)

type tapeReadSession struct {
	backend      *mediaBackend
	media        *mediapkg.Descriptor
	capabilities mediapkg.Capabilities
	device       string
	mountPoint   string
	tapeDir      string
	recycleKey   func()
	indexPath    string
}

func (b *mediaBackend) newTapeReadSession(
	ctx context.Context,
	target *entity.ReadTapeTarget,
	expectation *entity.ReadMediaTarget,
) (result mediapkg.ReadSession, returnErr error) {
	// Reserve the configured drive and resolve the loaded Tape identity.
	// The active attempt owns the lease through settlement, including failed construction.
	device := strings.TrimSpace(target.GetDevice())
	if device == "" {
		return nil, fmt.Errorf("restore Tape device is empty")
	}
	if err := b.acquireDevice(ctx, func(ctx context.Context, onWait func()) error {
		return b.executor.AcquireTapeDevice(ctx, b.jobID, device, onWait)
	}); err != nil {
		return nil, err
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
	defer func() {
		if result == nil {
			recycleKey()
		}
	}()
	if err := b.configureTape(ctx, device, keyPath, barcode, stored.Name, tapeDir); err != nil {
		return nil, err
	}
	mountPoint, err := b.mountTape(ctx, device, tapeDir)
	if err != nil {
		return nil, err
	}

	// The returned Session now owns the mount and key until Finalize.
	return &tapeReadSession{
		backend: b, media: describeMedia(stored), capabilities: capabilities, mountPoint: mountPoint,
		device: device, tapeDir: tapeDir, recycleKey: recycleKey, indexPath: indexPath,
	}, nil
}

func (s *tapeReadSession) Capabilities() mediapkg.Capabilities { return s.capabilities }

func (s *tapeReadSession) Media() *mediapkg.Descriptor { return s.media }

func (s *tapeReadSession) SourcePath(relative string) (string, error) {
	return mediapkg.ResolveSourcePath(s.mountPoint, relative)
}

func (s *tapeReadSession) Finalize(ctx context.Context) error {
	// Identity was validated before mounting and the attempt still owns the drive.
	// Re-running ReadInfo would issue mt load while LTFS is using that cartridge.
	defer s.recycleKey()
	return s.backend.unmountTape(ctx, s.device, s.mountPoint, s.tapeDir)
}

func (s *tapeReadSession) WalkInventory(ctx context.Context, yield func(*mediapkg.InventoryEntry) error) error {
	// The mount-time LTFS index supplies authoritative paths and physical extents; live stat supplies read facts.
	err := s.readInventory(ctx, func(entry *mediapkg.LTFSIndexEntry) error {
		filename, err := s.SourcePath(entry.Path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(filename)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("Tape inventory path is not an ordinary file: %q", entry.Path)
		}
		return yield(&mediapkg.InventoryEntry{Path: entry.Path, Info: info, Storage: entry.Storage})
	})
	return err
}

func (s *tapeReadSession) readInventory(
	ctx context.Context, yield func(*mediapkg.LTFSIndexEntry) error,
) (returnErr error) {
	// Keep the captured index open only while its entries are being consumed.
	if err := waitForTapeIndex(ctx, s.indexPath); err != nil {
		return err
	}
	file, err := os.Open(s.indexPath)
	if err != nil {
		return err
	}
	defer func() {
		if err := file.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close read LTFS index failed, %w", err))
		}
	}()
	return mediapkg.ParseLTFSIndex(ctx, file, func(entry *mediapkg.LTFSIndexEntry) error {
		if yield != nil {
			return yield(entry)
		}
		return nil
	})
}

type tapeWriteSession struct {
	backend      *mediaBackend
	media        *mediapkg.Descriptor
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
	target *entity.ArchiveTapeTarget,
) (result mediapkg.WriteSession, returnErr error) {
	// Reserve the requested drive and reject a different loaded Tape before any mutation.
	// The active attempt owns the lease through settlement, including failed construction.
	device := strings.TrimSpace(target.GetDevice())
	if device == "" {
		return nil, fmt.Errorf("archive Tape device is empty")
	}
	barcode, err := NormalizeTapeBarcode(target.GetBarcode())
	if err != nil {
		return nil, err
	}
	if err := b.acquireDevice(ctx, func(ctx context.Context, onWait func()) error {
		return b.executor.AcquireTapeDevice(ctx, b.jobID, device, onWait)
	}); err != nil {
		return nil, err
	}
	deviceBarcode, err := b.executor.ReadTapeBarcode(ctx, device)
	if err != nil {
		return nil, err
	}
	if deviceBarcode == "" && target.GetMode() != entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT {
		return nil, fmt.Errorf("archive Tape identity is unavailable")
	}
	if deviceBarcode != "" && deviceBarcode != barcode {
		return nil, fmt.Errorf("archive Tape changed, requested=%q device=%q", barcode, deviceBarcode)
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
	defer func() {
		if result == nil {
			recycleKey()
		}
	}()
	tapeDir, err := b.executor.EnsureTapeWorkPath(ctx, b.jobID, barcode)
	if err != nil {
		return nil, err
	}
	indexPath := filepath.Join(tapeDir, barcode+".schema")
	if err := os.Remove(indexPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale LTFS index failed, barcode=%q, %w", barcode, err)
	}

	// Configure, optionally format, and mount the physical Tape.
	if err := b.configureTape(ctx, device, keyPath, barcode, pendingMedia.Name, tapeDir); err != nil {
		return nil, err
	}
	if format {
		if err := b.formatTape(ctx, device, barcode, pendingMedia.Name, tapeDir); err != nil {
			return nil, err
		}
	}
	mountPoint, err := b.mountTape(ctx, device, tapeDir)
	if err != nil {
		return nil, err
	}
	defer func() {
		if result == nil {
			returnErr = errors.Join(returnErr, b.unmountTape(context.WithoutCancel(ctx), device, mountPoint, tapeDir))
		}
	}()

	// Discard the mount-time snapshot so only the post-unmount Index can satisfy finalization.
	if err := os.Remove(indexPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove mounted LTFS index failed, barcode=%q, %w", barcode, err)
	}
	pathPrefix := "."
	if !format {
		pathPrefix, err = allocateMediaPathPrefix(mountPoint, uint64(time.Now().UnixNano()))
		if err != nil {
			return nil, err
		}
	}

	// Transfer only the physical Session resources; the attempt still owns its device lease.
	return &tapeWriteSession{
		backend: b, media: describeMedia(pendingMedia),
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
			CreatedAtNS: time.Now().UnixNano(),
		}, keyPath, recycleKey, true, nil
	case entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND:
		if stored == nil {
			return nil, "", nil, false, fmt.Errorf("append Tape is not in Library, barcode=%q", barcode)
		}
		profile := stored.Profile.GetTape()
		if profile == nil || profile.Format != library.TapeFormatLTFSV1 || stored.DestroyedAtNS != nil {
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

func (s *tapeWriteSession) Media() *mediapkg.Descriptor { return s.media }

func (s *tapeWriteSession) TargetPath(relative string, _ int64) (string, error) {
	if err := entity.ValidateRelativePath(relative); err != nil {
		return "", err
	}
	mediaPath := filepath.ToSlash(filepath.Join(s.pathPrefix, relative))
	return mediapkg.ResolveTargetPath(s.mountPoint, mediaPath)
}

func (s *tapeWriteSession) Finalize(ctx context.Context, targetFull bool) (*mediapkg.WriteResult, error) {
	// Establish the physical durability boundary before consulting any captured Index.
	defer s.recycleKey()
	if err := s.backend.unmountTape(ctx, s.device, s.mountPoint, s.tapeDir); err != nil {
		return nil, errors.Join(mediapkg.ErrFinalizeUnusable, err)
	}

	// Expose the post-unmount Index as a bounded physical source; Archive owns reconciliation.
	inventory, err := s.finalInventory(ctx)
	if err != nil {
		return nil, errors.Join(mediapkg.ErrFinalizeUnusable, err)
	}
	return &mediapkg.WriteResult{
		Media: s.media, PathPrefix: s.pathPrefix, VerifiedPrefix: targetFull, Inventory: inventory,
	}, nil
}

var (
	errInvalidTapeIndex   = errors.New("invalid final Tape index")
	errUnusableTapeResult = errors.New("unusable Tape finalize result")
)

func (s *tapeWriteSession) finalInventory(ctx context.Context) (result mediapkg.FinalInventory, returnErr error) {
	// Validate the capture without retaining an open file in the returned inventory callback.
	if err := waitForTapeIndex(ctx, s.indexPath); err != nil {
		return nil, errors.Join(
			errInvalidTapeIndex, errUnusableTapeResult,
			fmt.Errorf("wait for final LTFS index failed, barcode=%q, %w", s.barcode, err),
		)
	}
	file, err := os.Open(s.indexPath)
	if err != nil {
		return nil, errors.Join(
			errInvalidTapeIndex, errUnusableTapeResult,
			fmt.Errorf("open LTFS index failed, barcode=%q, %w", s.barcode, err),
		)
	}
	defer func() {
		if err := file.Close(); err != nil {
			result = nil
			returnErr = errors.Join(returnErr, fmt.Errorf("close LTFS index failed, barcode=%q, %w", s.barcode, err))
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(
			errInvalidTapeIndex, errUnusableTapeResult,
			fmt.Errorf("stat LTFS index failed, barcode=%q, %w", s.barcode, err),
		)
	}
	if info.Size() == 0 {
		return nil, errors.Join(
			errInvalidTapeIndex, errUnusableTapeResult,
			fmt.Errorf("LTFS index is empty, barcode=%q", s.barcode),
		)
	}

	// Each consumption owns its own handle, including parser and callback failures.
	return func(ctx context.Context, yield func(*mediapkg.LTFSIndexEntry) error) (returnErr error) {
		file, err := os.Open(s.indexPath)
		if err != nil {
			return fmt.Errorf("open final LTFS index failed, barcode=%q, %w", s.barcode, err)
		}
		defer func() {
			if err := file.Close(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close final LTFS index failed, barcode=%q, %w", s.barcode, err))
			}
		}()
		return mediapkg.ParseLTFSIndex(ctx, file, yield)
	}, nil
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

func (b *mediaBackend) mountTape(ctx context.Context, device, tapeDir string) (result string, returnErr error) {
	// A failed or canceled script can still have mounted LTFS; register physical cleanup first.
	mountPoint, err := os.MkdirTemp("", "yatm-ltfs-*")
	if err != nil {
		return "", fmt.Errorf("create Tape mount point failed, %w", err)
	}
	defer func() {
		if result == "" {
			returnErr = errors.Join(returnErr, b.unmountTape(context.WithoutCancel(ctx), device, mountPoint, tapeDir))
		}
	}()

	// A successful mount transfers its directory and unmount obligation to the Session builder.
	cmd := exec.CommandContext(ctx, b.executor.Scripts().Mount)
	cmd.Env = append(cmd.Env,
		fmt.Sprintf("DEVICE=%s", device), fmt.Sprintf("MOUNT_POINT=%s", mountPoint),
		fmt.Sprintf("TAPE_DIR=%s", tapeDir),
	)
	if err := tools.RunCmd(b.logger, cmd); err != nil {
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
