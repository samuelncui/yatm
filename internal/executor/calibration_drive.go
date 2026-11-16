package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
)

// CalibrationDrive is a calibration attempt's native session on the loaded cartridge. It moves the
// tape only through LOCATE and never reads records, mounts LTFS or runs a Tape script.
type CalibrationDrive struct {
	// Report is the drive inspection, including its serial number and the recorded density.
	Report *entity.InspectDeviceResponse
	// Endpoints is the current wrap endpoint map read planning uses; empty when the drive reports
	// no complete map.
	Endpoints []tape.WrapEndpoint
	// RecordBytes is the record size of the Media or of the written test data.
	RecordBytes uint32
	// Builtin is the built-in read parameter set for the recorded density.
	Builtin *entity.TapeReadParameters

	exe        *Executor
	jobID      int64
	device     string
	prepared   *tape.Device
	navigation *tape.Navigation
}

// OpenCalibrationDrive opens the leased device on the native path a tar read uses and checks that
// the loaded cartridge is the given Media under the tar read's barcode check. The caller holds the
// attempt's lease and owns Finish or Cleanup of a returned drive.
func (e *Executor) OpenCalibrationDrive(ctx context.Context, jobID int64, device string, media *library.Media) (drive *CalibrationDrive, returnErr error) {
	// Positions and record bytes are only linear in tape length for uncompressed tar records.
	profile := media.Profile.GetTape()
	if media.Kind != entity.MediaKind_MEDIA_KIND_TAPE || profile.GetFormat() != library.TapeFormatTarPAXV1 || profile.GetTar() == nil {
		return nil, fmt.Errorf("Calibration requires tar Tape Media, media_id=%d", media.ID)
	}
	if profile.Tar.HardwareCompression {
		return nil, fmt.Errorf("Calibration requires tar Media written with hardware compression off, media_id=%d", media.ID)
	}

	// The label is encrypted by the Tape script, so the cartridge memory barcode is the identity check.
	prepared, err := e.openRawTape(ctx, jobID, device)
	if err != nil {
		return nil, err
	}
	defer func() {
		if drive == nil {
			returnErr = errors.Join(returnErr, e.cleanupRawTape(ctx, jobID, device, prepared))
		}
	}()
	if err := crossCheckTarBarcode(ctx, prepared, media.Identity, false); err != nil {
		return nil, err
	}
	return e.calibrationDrive(ctx, jobID, device, prepared, profile.Tar)
}

// calibrationTestDataChunkBytes is how much test data is written between two wrap reports.
const calibrationTestDataChunkBytes = 4 << 30

// OpenCalibrationTestDrive overwrites the loaded cartridge from the beginning of tape with zero
// records until the drive's wrap report shows wraps 0 and 1 complete, and returns the drive ready
// to calibrate on them. Like FORMAT, it refuses a cartridge whose barcode is registered Media and
// examines nothing else. The caller holds the attempt's lease.
func (e *Executor) OpenCalibrationTestDrive(ctx context.Context, jobID int64, device string, logger *logrus.Logger) (*CalibrationDrive, error) {
	options := mergeTarOptions(&entity.TarWriteOptions{HardwareCompression: proto.Bool(false)})
	profile := &entity.TarMediaProfile{RecordMode: options.GetRecordMode(), RecordBytes: options.GetRecordBytes()}
	return e.writeCalibrationTestData(ctx, jobID, device, profile, calibrationTestDataChunkBytes, logger)
}

func (e *Executor) writeCalibrationTestData(ctx context.Context, jobID int64, device string, profile *entity.TarMediaProfile,
	chunkBytes uint64, logger *logrus.Logger,
) (drive *CalibrationDrive, returnErr error) {
	prepared, err := e.openRawTape(ctx, jobID, device)
	if err != nil {
		return nil, err
	}
	defer func() {
		if drive == nil && prepared != nil {
			returnErr = errors.Join(returnErr, e.cleanupRawTape(ctx, jobID, device, prepared))
		}
	}()

	// FORMAT's only identity guard: a cartridge registered as Media is never overwritten.
	value, err := prepared.Barcode(ctx)
	if err != nil {
		return nil, err
	}
	barcode, err := normalizeReportedTapeBarcode(value)
	if err != nil {
		return nil, err
	}
	if barcode != "" {
		stored, err := e.Lib().GetMediaByIdentity(ctx, entity.MediaKind_MEDIA_KIND_TAPE, barcode)
		if err != nil {
			return nil, err
		}
		if err := checkTapeWriteMedia(stored, &entity.ArchiveTapeTarget{Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT, Barcode: barcode}); err != nil {
			return nil, err
		}
	}

	// FORMAT's preparation and initialization establish a single partition at the beginning of tape.
	records, err := prepared.Configure(ctx, profile, tape.Format)
	if err != nil {
		return nil, err
	}
	if err := prepared.Initialize(ctx); err != nil {
		return nil, err
	}
	data := make([]byte, profile.RecordBytes)
	written := uint64(0)
	for {
		for chunk := uint64(0); chunk < chunkBytes; chunk += uint64(len(data)) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if _, err := records.Write(ctx, data); err != nil {
				return nil, fmt.Errorf("write calibration test data failed, %w", err)
			}
			written += uint64(len(data))
		}
		if err := records.Filemarks(ctx, 0); err != nil {
			return nil, fmt.Errorf("synchronize calibration test data failed, %w", err)
		}

		// A fresh descriptor reads the drive's current wrap report of the written data.
		err := e.cleanupRawTape(ctx, jobID, device, prepared)
		prepared = nil
		if err != nil {
			return nil, err
		}
		if prepared, err = e.openRawTape(ctx, jobID, device); err != nil {
			return nil, err
		}
		opened, err := e.calibrationDrive(ctx, jobID, device, prepared, profile)
		if err != nil {
			return nil, err
		}
		logger.WithFields(logrus.Fields{"job_id": jobID, "written_bytes": written, "wrap_endpoints": len(opened.Endpoints)}).
			Info("Calibration test data written")

		// The last endpoint is end of data, so a third one means wraps 0 and 1 are complete.
		endpoints := opened.Endpoints
		if len(endpoints) >= 3 {
			return opened, nil
		}
		if len(endpoints) == 0 {
			return nil, errors.New("Calibration needs the drive's current wrap endpoint map, which is unavailable")
		}
		position, err := opened.Position(ctx)
		if err != nil {
			return nil, err
		}
		if end := endpoints[len(endpoints)-1].Object; position != end {
			return nil, fmt.Errorf("Tape position %d after calibration test data differs from end of data %d", position, end)
		}
		if records, err = prepared.Configure(ctx, profile, tape.Append); err != nil {
			return nil, err
		}
	}
}

// calibrationDrive prepares an opened descriptor for LOCATE and READ POSITION and reads its wrap
// endpoint map. The caller cleans up the descriptor when it fails.
func (e *Executor) calibrationDrive(ctx context.Context, jobID int64, device string, prepared *tape.Device, profile *entity.TarMediaProfile) (*CalibrationDrive, error) {
	if _, err := prepared.Configure(ctx, profile, tape.Read); err != nil {
		return nil, err
	}
	navigation, err := prepared.Navigation(ctx)
	if err != nil {
		return nil, err
	}
	if navigation == nil {
		return nil, errors.New("Calibration needs an eligible standard READ POSITION/LOCATE pairing")
	}

	// The position source is the same wrap endpoint map read planning uses.
	geometry, err := prepared.Geometry(ctx)
	if err != nil {
		return nil, err
	}
	report := prepared.Report()
	drive := &CalibrationDrive{
		Report: report, RecordBytes: profile.RecordBytes, Builtin: builtinTapeReadParameters(report),
		exe: e, jobID: jobID, device: device, prepared: prepared, navigation: navigation,
	}
	if geometry.Complete() {
		if reason := tarReportedWrapsReason(geometry); reason != "" {
			return nil, errors.New(reason)
		}
		drive.Endpoints = geometry.Endpoints
	}
	return drive, nil
}

// Serial is the drive's reported serial number, or empty when unknown.
func (d *CalibrationDrive) Serial() string { return strings.TrimSpace(d.Report.GetSerialNumber()) }

// Locate issues one LOCATE to a partition-zero logical object.
func (d *CalibrationDrive) Locate(ctx context.Context, object uint64) error {
	return d.navigation.Locate(ctx, 0, object)
}

// Position reads the current partition-zero logical object.
func (d *CalibrationDrive) Position(ctx context.Context) (uint64, error) {
	position, err := d.navigation.Position(ctx)
	if err != nil {
		return 0, err
	}
	if position.Partition != 0 || !position.ObjectKnown {
		return 0, errors.New("Tape position is unknown or outside partition zero")
	}
	return position.Object, nil
}

// Finish restores the session controls and unloads the cartridge through the tar finish path.
func (d *CalibrationDrive) Finish(ctx context.Context) error {
	return d.exe.releaseRawTape(ctx, d.jobID, d.device, d.prepared, true)
}

// Cleanup restores the session controls without unloading, as failed tar work does.
func (d *CalibrationDrive) Cleanup(ctx context.Context) error {
	return d.exe.releaseRawTape(ctx, d.jobID, d.device, d.prepared, false)
}
