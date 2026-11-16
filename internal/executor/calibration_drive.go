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
)

// CalibrationDrive is a calibration attempt's native session on the loaded cartridge. It moves the
// tape only through LOCATE and never reads records, mounts LTFS or runs a Tape script.
type CalibrationDrive struct {
	// Report is the drive inspection, including its serial number and the recorded density.
	Report *entity.InspectDeviceResponse
	// Endpoints is the wrap endpoint map of the partition being calibrated, the one holding the
	// most wraps; empty when the drive reports no complete map.
	Endpoints []tape.WrapEndpoint
	// WrapBytes is the nominal native capacity of one wrap of the recorded density; zero when the
	// density is unknown.
	WrapBytes float64
	// Builtin is the built-in read parameter set for the recorded density.
	Builtin *entity.TapeReadParameters

	exe        *Executor
	jobID      int64
	device     string
	prepared   *tape.Device
	navigation *tape.Navigation
	partition  uint32
}

// OpenCalibrationDrive loads the cartridge in the leased device on the native path a tar read uses
// and calibrates on whatever it holds; neither the Library nor the cartridge's contents are consulted. The caller
// holds the attempt's lease and owns Finish of a returned drive; a failure unloads the cartridge.
func (e *Executor) OpenCalibrationDrive(ctx context.Context, jobID int64, device string) (drive *CalibrationDrive, returnErr error) {
	prepared, err := e.loadRawTape(ctx, jobID, device)
	if err != nil {
		return nil, errors.Join(err, e.unloadCancelledCalibration(ctx, jobID, device, err))
	}
	defer func() {
		if drive == nil {
			returnErr = errors.Join(returnErr, e.releaseRawTape(ctx, jobID, device, prepared, true))
		}
	}()
	// No record is read, so the record policy only has to be one every drive accepts.
	profile := &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 4096}
	return e.calibrationDrive(ctx, jobID, device, prepared, profile)
}

// unloadCancelledCalibration unloads the cartridge when a cancellation ended the load: the LOAD may
// already have completed, and a cancelled Calibration leaves the cartridge unloaded.
func (e *Executor) unloadCancelledCalibration(ctx context.Context, jobID int64, device string, cause error) error {
	if ctx.Err() == nil {
		return nil
	}
	return e.unloadWithoutDescriptor(ctx, jobID, device, cause)
}

// unloadWithoutDescriptor unloads the cartridge through a new descriptor after a failure that
// holds none. An uncertain command outcome, or a drive that cannot be opened, leaves it as it is.
func (e *Executor) unloadWithoutDescriptor(ctx context.Context, jobID int64, device string, cause error) error {
	if errors.Is(cause, tape.ErrUncertain) {
		return nil
	}
	ctx = context.WithoutCancel(ctx)
	prepared, err := e.openRawTape(ctx, jobID, device)
	if err != nil {
		if errors.Is(err, tape.ErrUncertain) {
			return err
		}
		return nil
	}
	return e.releaseRawTape(ctx, jobID, device, prepared, true)
}

// calibrationCartridgeMedia returns the Library Tape Media registered under the loaded cartridge's
// barcode, or nil when the barcode is empty or unregistered.
func (e *Executor) calibrationCartridgeMedia(ctx context.Context, prepared *tape.Device) (*library.Media, string, error) {
	value, err := prepared.Barcode(ctx)
	if err != nil {
		return nil, "", err
	}
	barcode, err := normalizeReportedTapeBarcode(value)
	if err != nil || barcode == "" {
		return nil, barcode, err
	}
	stored, err := e.Lib().GetMediaByIdentity(ctx, entity.MediaKind_MEDIA_KIND_TAPE, barcode)
	return stored, barcode, err
}

// calibrationTestDataChunkBytes is how much test data is written between two wrap reports.
const calibrationTestDataChunkBytes = 4 << 30

// OpenCalibrationTestDrive overwrites the loaded cartridge from the beginning of tape with zero
// records until the drive's wrap report shows wraps 0 and 1 complete, and returns the drive ready
// to calibrate on them. Like FORMAT, it refuses a cartridge whose barcode is registered Media and
// examines nothing else; that refusal leaves the cartridge loaded and any later failure unloads it.
// The caller holds the attempt's lease.
func (e *Executor) OpenCalibrationTestDrive(ctx context.Context, jobID int64, device string, logger *logrus.Logger) (*CalibrationDrive, error) {
	options := mergeTarOptions()
	profile := &entity.TarMediaProfile{RecordMode: options.GetRecordMode(), RecordBytes: options.GetRecordBytes()}
	return e.writeCalibrationTestData(ctx, jobID, device, profile, calibrationTestDataChunkBytes, logger)
}

func (e *Executor) writeCalibrationTestData(ctx context.Context, jobID int64, device string, profile *entity.TarMediaProfile,
	chunkBytes uint64, logger *logrus.Logger,
) (drive *CalibrationDrive, returnErr error) {
	prepared, err := e.loadRawTape(ctx, jobID, device)
	if err != nil {
		return nil, errors.Join(err, e.unloadCancelledCalibration(ctx, jobID, device, err))
	}
	admitted := false
	defer func() {
		if drive == nil && prepared != nil {
			returnErr = errors.Join(returnErr, e.releaseRawTape(ctx, jobID, device, prepared, admitted))
		}
	}()

	// The only identity guard: a cartridge registered as Media is never overwritten.
	stored, barcode, err := e.calibrationCartridgeMedia(ctx, prepared)
	if err != nil {
		return nil, err
	}
	if stored != nil {
		return nil, fmt.Errorf("the cartridge is Media in the Library and test data would overwrite it; turn off writing test data to calibrate on it, or use another cartridge, barcode=%q", barcode)
	}
	admitted = true

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
				return nil, fmt.Errorf("write calibration test data failed, %w; check the cartridge is writable and run the Calibration again", err)
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
			// No descriptor is held between two chunks, so the cartridge is unloaded through a new one.
			return nil, errors.Join(err, e.unloadWithoutDescriptor(ctx, jobID, device, err))
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
			return nil, errors.New("Calibration needs the drive's current wrap endpoint map, which is unavailable; this drive or cartridge cannot be calibrated: try another cartridge, or keep the current read parameters or edit them in Device settings")
		}
		position, err := opened.Position(ctx)
		if err != nil {
			return nil, err
		}
		if end := endpoints[len(endpoints)-1].Object; position != end {
			return nil, fmt.Errorf("Tape position %d after calibration test data differs from end of data %d; run the Calibration again", position, end)
		}
		if records, err = prepared.Configure(ctx, profile, tape.Append); err != nil {
			return nil, err
		}
	}
}

// calibrationDrive prepares an opened descriptor for LOCATE and READ POSITION and reads its wrap
// endpoint map. The caller cleans up the descriptor when it fails.
func (e *Executor) calibrationDrive(ctx context.Context, jobID int64, device string, prepared *tape.Device, profile *entity.TarMediaProfile) (*CalibrationDrive, error) {
	if _, err := prepared.Configure(ctx, profile, tape.Navigate); err != nil {
		return nil, err
	}
	navigation, err := prepared.Navigation(ctx)
	if err != nil {
		return nil, err
	}
	if navigation == nil {
		return nil, errors.New("Calibration needs an eligible standard READ POSITION/LOCATE pairing, which this drive does not report; it cannot be calibrated: keep the current read parameters or edit them in Device settings")
	}

	// The position source is the same wrap endpoint map read planning uses.
	geometry, err := prepared.Geometry(ctx)
	if err != nil {
		return nil, err
	}
	report := prepared.Report()
	drive := &CalibrationDrive{
		Report: report, Builtin: builtinTapeReadParameters(report), WrapBytes: nominalWrapBytes(report.GetTape().DensityCode),
		exe: e, jobID: jobID, device: device, prepared: prepared, navigation: navigation,
	}
	if !geometry.Complete() {
		return drive, nil
	}
	endpoints, err := calibrationPartition(geometry.Endpoints)
	if err != nil {
		return nil, err
	}
	drive.Endpoints, drive.partition = endpoints, endpoints[0].Partition

	// The sequence starts at the beginning of the calibrated partition.
	if drive.partition != 0 {
		if err := navigation.Locate(ctx, drive.partition, 0); err != nil {
			return nil, fmt.Errorf("locate the beginning of partition %d failed, %w; run the Calibration again", drive.partition, err)
		}
	}
	return drive, nil
}

// calibrationPartition returns the endpoints of the partition holding the most wraps: partition
// zero of a tar cartridge, the data partition of an LTFS one. Its wraps must be contiguous and
// begin on a forward wrap, as the calibration sequence assumes.
func calibrationPartition(all []tape.WrapEndpoint) ([]tape.WrapEndpoint, error) {
	var best []tape.WrapEndpoint
	for start := 0; start < len(all); {
		end := start + 1
		for end < len(all) && all[end].Partition == all[start].Partition {
			end++
		}
		if end-start > len(best) {
			best = all[start:end]
		}
		start = end
	}
	if best[0].Wrap%2 != 0 {
		return nil, fmt.Errorf("Calibration needs a partition that begins on a forward wrap, partition=%d wrap=%d; calibrate another cartridge, or a spare one by writing test data", best[0].Partition, best[0].Wrap)
	}
	low := uint64(0)
	for index, endpoint := range best {
		if endpoint.Wrap != best[0].Wrap+uint32(index) || endpoint.Object <= low {
			return nil, errors.New("Reported wrap boundaries are not contiguous, or are empty or reversed; calibrate another cartridge, or a spare one by writing test data")
		}
		low = endpoint.Object
	}
	return best, nil
}

// Serial is the drive's reported serial number, or empty when unknown.
func (d *CalibrationDrive) Serial() string { return strings.TrimSpace(d.Report.GetSerialNumber()) }

// Locate issues one LOCATE to a logical object of the calibrated partition.
func (d *CalibrationDrive) Locate(ctx context.Context, object uint64) error {
	return d.navigation.Locate(ctx, d.partition, object)
}

// Position reads the current logical object in the calibrated partition.
func (d *CalibrationDrive) Position(ctx context.Context) (uint64, error) {
	position, err := d.navigation.Position(ctx)
	if err != nil {
		return 0, err
	}
	if position.Partition != d.partition || !position.ObjectKnown {
		return 0, errors.New("Tape position is unknown or outside the calibrated partition; run the Calibration again")
	}
	return position.Object, nil
}

// Finish restores the session controls and unloads the cartridge, whether or not the
// Calibration succeeded, as tar work does.
func (d *CalibrationDrive) Finish(ctx context.Context) error {
	return d.exe.releaseRawTape(ctx, d.jobID, d.device, d.prepared, true)
}
