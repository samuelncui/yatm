package tape

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

var ErrEarlyWarning = errors.New("Tape early warning")

// RecordMode is the explicitly observed/frozen mode, not a request to change drive controls.
// WarningCounts qualifies current WRITE count forms (GA32-0928-08 §4.12) and synchronous
// FILEMARK completion under the established SSC baseline. The caller establishes applicability;
// fake transport tests do not qualify hardware.
type RecordMode struct {
	FixedBytes    int
	MaximumBytes  int
	WarningCounts bool
}

// RecordResult separates accepted-byte evidence from boundary/warning flags. A warning with an
// unknown count cannot acknowledge a record even when the SG residual is zero.
type RecordResult struct {
	Bytes                       int
	AcceptedKnown               bool
	Filemark, EOD, EarlyWarning bool
	Sense                       Sense
}

// Records uses one SCSI command per record, independent of ACP chunks. Commands remain serialized
// by its Transport; callers freeze mode before constructing this reader/writer.
type Records struct {
	Transport          Transport
	Mode               RecordMode
	Timeout            time.Duration
	invalidateGeometry func()
}

func (r Records) command(opcode byte, data []byte, direction Direction) (Command, error) {
	// Fixed transfers count records; variable transfers count bytes. Neither silently truncates 24 bits.
	if r.Mode.MaximumBytes <= 0 || r.Mode.MaximumBytes > MaxTransferBytes || len(data) == 0 || len(data) > r.Mode.MaximumBytes {
		return Command{}, fmt.Errorf("record size is outside the supported envelope")
	}
	count, fixed := len(data), byte(0)
	if r.Mode.FixedBytes != 0 {
		if len(data) != r.Mode.FixedBytes {
			return Command{}, fmt.Errorf("fixed transfer must contain exactly one record")
		}
		count, fixed = 1, 1
	}
	cdb := []byte{opcode, fixed, byte(count >> 16), byte(count >> 8), byte(count), 0}
	return Command{CDB: cdb, Direction: direction, Data: data, Timeout: r.Timeout}, nil
}

func (r Records) Read(ctx context.Context, data []byte) (result RecordResult, err error) {
	defer func() { r.geometryError(err) }()
	// SILI is deliberately clear: short variable records must expose signed ILI evidence.
	command, err := r.command(0x08, data, FromDevice)
	if err != nil {
		return RecordResult{}, err
	}
	command.readDiscard, _ = ctx.Value(readDiscardContextKey{}).(bool)
	command.validate = func(response Response) error { _, err := decodeRecord(response, len(data), r.Mode, false); return err }
	response, commandErr := r.Transport.Execute(ctx, command)
	if !response.Completed && commandErr != nil {
		return RecordResult{}, commandErr
	}
	result, err = decodeRecord(response, len(data), r.Mode, false)
	if errors.Is(commandErr, ErrUncertain) {
		result.Bytes, result.AcceptedKnown = 0, false
	}
	err = errors.Join(err, commandErr)
	if (result.Filemark || result.EOD) && commandErr == nil {
		return result, nil
	}
	return result, err
}

func (r Records) Write(ctx context.Context, data []byte) (RecordResult, error) {
	return r.write(ctx, data, nil, func(ctx context.Context, command Command) (Response, error) {
		return r.Transport.Execute(ctx, command)
	})
}

// WriteObserved records actual WRITE issue under the primary WRITE lane for its accounting callback.
// A callback rejection proves zero acceptance; no admission lock spans the native command.
func (r Records) WriteObserved(ctx context.Context, data []byte, onIssue func() error) (RecordResult, error) {
	if onIssue == nil {
		return r.Write(ctx, data)
	}

	// Qualify actual issue accounting before allowing any WRITE onto an opaque transport.
	transport, ok := r.Transport.(interface {
		ExecuteObserved(context.Context, Command, func() error) (Response, error)
	})
	if !ok {
		return RecordResult{AcceptedKnown: true}, fmt.Errorf("WRITE issue observation is unavailable: %w", ErrUnsupported)
	}
	return r.write(ctx, data, onIssue, func(ctx context.Context, command Command) (Response, error) {
		return transport.ExecuteObserved(ctx, command, command.onIssue)
	})
}

func (r Records) write(ctx context.Context, data []byte, onIssue func() error, execute func(context.Context, Command) (Response, error)) (RecordResult, error) {
	// Validate a complete record before admitting a state-changing command.
	if r.invalidateGeometry != nil {
		r.invalidateGeometry()
	}
	command, err := r.command(0x0a, data, ToDevice)
	if err != nil {
		return RecordResult{}, err
	}
	command.validate = func(response Response) error { _, err := decodeRecord(response, len(data), r.Mode, true); return err }
	var issueErr error
	if onIssue != nil {
		command.onIssue = func() error { issueErr = onIssue(); return issueErr }
	}
	response, commandErr := execute(ctx, command)
	if !response.Completed && commandErr != nil {
		// The serialized transport returns plain cancellation only before command admission.
		if commandErr == context.Canceled || commandErr == context.DeadlineExceeded || issueErr != nil && !errors.Is(commandErr, ErrUncertain) {
			return RecordResult{AcceptedKnown: true}, commandErr
		}
		return RecordResult{}, commandErr
	}
	result, err := decodeRecord(response, len(data), r.Mode, true)
	if errors.Is(commandErr, ErrUncertain) {
		result.Bytes, result.AcceptedKnown = 0, false
	}
	return result, errors.Join(err, commandErr)
}

func decodeRecord(response Response, size int, mode RecordMode, writing bool) (RecordResult, error) {
	// Host transfer counts are bounded evidence; successful full records need consistent completion.
	if response.Residual < 0 || int64(response.Residual) > int64(size) {
		return RecordResult{}, &UncertainError{Cause: ErrMalformed}
	}
	err := CheckResponse(response)
	if err == nil {
		if response.Residual != 0 {
			return RecordResult{}, &UncertainError{Cause: io.ErrUnexpectedEOF}
		}
		return RecordResult{Bytes: size, AcceptedKnown: true}, nil
	}
	var failure *CommandError
	if !errors.As(err, &failure) {
		return RecordResult{}, err
	}
	sense := failure.Sense
	result := RecordResult{Sense: sense, EarlyWarning: writing && earlyWarningSense(sense)}
	if sense.Deferred {
		return result, &UncertainError{Cause: err}
	}

	// Qualified current warnings preserve exactly the documented untransferred-unit count.
	if writing {
		if rejectedCommand(err) {
			result.AcceptedKnown = true
			return result, err
		}
		remaining, warningErr := writeWarningRemaining(failure, mode)
		if warningErr != nil {
			return result, &UncertainError{Cause: warningErr}
		}
		if remaining != 0 && remaining != int64(size) {
			return result, &UncertainError{Cause: ErrMalformed}
		}
		result.Bytes, result.AcceptedKnown = size-int(remaining), true
		return result, errors.Join(ErrEarlyWarning, err)
	}

	// Standard read boundaries never substitute the requested length for the actual residual.
	if sense.Filemark && sense.ILI || sense.ILI && sense.EOM {
		return result, &UncertainError{Cause: ErrMalformed}
	}
	result.Filemark = sense.Filemark && sense.Key == 0 && sense.ASC == 0 && sense.ASCQ == 1
	result.EOD = sense.Key == 8 && sense.ASC == 0 && sense.ASCQ == 5
	if result.Filemark || result.EOD {
		if response.Residual != int32(size) {
			return RecordResult{Sense: sense}, &UncertainError{Cause: ErrMalformed}
		}
		remaining := int64(size)
		if mode.FixedBytes != 0 {
			remaining = 1
		}
		if result.Filemark && !sense.InformationValid || sense.InformationValid && sense.Information != remaining {
			return RecordResult{Sense: sense}, &UncertainError{Cause: ErrMalformed}
		}
		result.AcceptedKnown = true
		return result, err
	}
	if sense.ILI && sense.Key == 0 && sense.ASC == 0 && sense.ASCQ == 0 && sense.InformationValid {
		if sense.Information < 0 {
			if mode.FixedBytes != 0 || response.Residual != 0 {
				return result, &UncertainError{Cause: ErrMalformed}
			}
			return result, errors.Join(io.ErrShortBuffer, err)
		}
		if mode.FixedBytes != 0 || sense.Information > int64(size) || sense.Information != int64(response.Residual) {
			return result, &UncertainError{Cause: ErrMalformed}
		}
		result.Bytes, result.AcceptedKnown = size-int(sense.Information), true
		return result, nil
	}
	if rejectedCommand(err) {
		return result, err
	}
	return result, &UncertainError{Cause: err}
}

// WriteWarning reports whether every cause is compatible with a settled WRITE early warning.
// The caller separately requires known accepted bytes and an early-warning result. Settled caller
// cancellation is compatible; an independent transport failure never is.
func WriteWarning(err error, remaining int64, mode RecordMode) bool {
	if err == ErrEarlyWarning || err == context.Canceled || err == context.DeadlineExceeded {
		return true
	}
	if failure, ok := err.(*CommandError); ok {
		observed, cause := writeWarningRemaining(failure, mode)
		return cause == nil && observed == remaining
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return WriteWarning(wrapped.Unwrap(), remaining, mode)
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !WriteWarning(cause, remaining, mode) {
				return false
			}
		}
		return true
	}
	return false
}

func writeWarningRemaining(failure *CommandError, mode RecordMode) (int64, error) {
	// One current-sense policy serves raw decoding and classification of joined completion errors.
	if !currentWarning(failure, mode.WarningCounts) {
		return 0, failure
	}
	remaining := failure.Sense.Information
	if mode.FixedBytes != 0 {
		if remaining < 0 || remaining > 1 {
			return 0, ErrMalformed
		}
		remaining *= int64(mode.FixedBytes)
	}
	if int64(failure.Response.Residual) != remaining {
		return 0, ErrMalformed
	}
	return remaining, nil
}

func earlyWarningSense(sense Sense) bool {
	return sense.EOM && sense.Key == 0 && sense.ASC == 0 && (sense.ASCQ == 0 || sense.ASCQ == 2)
}

func currentWarning(failure *CommandError, qualified bool) bool {
	sense := failure.Sense
	return qualified && currentCommand(failure) && earlyWarningSense(sense) &&
		sense.InformationValid && !sense.ILI && !sense.Filemark
}

func rejectedCommand(err error) bool {
	var failure *CommandError
	if !errors.As(err, &failure) || !currentCommand(failure) || failure.Sense.Filemark || failure.Sense.EOM || failure.Sense.ILI {
		return false
	}
	return failure.Sense.Key == 2 || failure.Sense.Key == 5
}

func controlResponse(response Response) error {
	err := CheckResponse(response)
	if err == nil || rejectedCommand(err) {
		return err
	}
	return &UncertainError{Cause: err}
}

// Filemarks synchronizes previous buffered data with IMMED clear. Count zero is the SSC sync form;
// it writes no extra mark. Canonical ErrEarlyWarning proves synchronization and every requested mark
// completed; any other error leaves completion unusable even when it contains that warning.
func (r Records) Filemarks(ctx context.Context, count uint32) (err error) {
	defer func() { r.geometryError(err) }()
	if count != 0 && r.invalidateGeometry != nil {
		r.invalidateGeometry()
	}
	// Completion failures without proven rejection leave the buffered data/mark boundary unknown.
	if count > 0xffffff {
		return fmt.Errorf("filemark count exceeds 24-bit command")
	}
	response, err := r.Transport.Execute(ctx, Command{
		CDB: []byte{0x10, 0, byte(count >> 16), byte(count >> 8), byte(count), 0}, Direction: NoData, Timeout: r.Timeout,
		validate: func(response Response) error { return filemarkResponse(response, count, r.Mode.WarningCounts) },
	})
	if err != nil {
		return err
	}
	if response.Residual != 0 {
		return &UncertainError{Cause: ErrMalformed}
	}
	err = filemarkResponse(response, count, r.Mode.WarningCounts)
	return err
}

func filemarkResponse(response Response, count uint32, warningCounts bool) error {
	// Native rejection retains its existing outcome; other responses need command-specific completion.
	err := CheckResponse(response)
	if err == nil || rejectedCommand(err) {
		return err
	}
	var failure *CommandError
	if errors.Is(err, ErrUncertain) || !errors.As(err, &failure) || count > 1 {
		return &UncertainError{Cause: err}
	}

	// SSC-3 §6.9 completes IMMED=0 marks and buffered objects before status. Its successful EW
	// need not set VALID with SEW=0; ignore invalid information, but reject any valid remainder.
	sense := failure.Sense
	if warningCounts && currentCommand(failure) && earlyWarningSense(sense) && !sense.ILI && !sense.Filemark &&
		(!sense.InformationValid || sense.Information == 0) && response.Residual == 0 {
		return ErrEarlyWarning
	}
	return &UncertainError{Cause: err}
}

func (r Records) Sync(ctx context.Context) error { return r.Filemarks(ctx, 0) }

// Unload uses the synchronous SSC LOAD UNLOAD form. The drive performs its own rewind;
// no separate REWIND or medium-dependent verification follows successful release.
func (r Records) Unload(ctx context.Context) (err error) {
	if r.invalidateGeometry != nil {
		r.invalidateGeometry()
	}
	defer func() { r.geometryError(err) }()
	response, err := r.Transport.Execute(ctx, Command{
		CDB: []byte{0x1b, 0, 0, 0, 0, 0}, Direction: NoData, Timeout: r.Timeout, validate: controlResponse,
	})
	if err != nil {
		return err
	}
	return controlResponse(response)
}

func (r Records) Rewind(ctx context.Context) (err error) {
	defer func() { r.geometryError(err) }()
	response, err := r.Transport.Execute(ctx, Command{CDB: []byte{1, 0, 0, 0, 0, 0}, Direction: NoData, Timeout: r.Timeout, validate: controlResponse})
	if err != nil {
		return err
	}
	if err := CheckResponse(response); err != nil {
		return err
	}
	return nil
}

// Space uses SSC SPACE(6), including reverse counts; EOD ignores count and cannot prove a safe tail.
func (r Records) Space(ctx context.Context, code byte, count int32) (err error) {
	defer func() { r.geometryError(err) }()
	// Refuse unrepresentable motion before encoding its signed count.
	if code != 0 && code != 1 && code != 3 || count < -0x800000 || count > 0x7fffff {
		return fmt.Errorf("unsupported SPACE form")
	}
	if code == 3 {
		count = 0
	}

	// Submit one non-immediate motion and resolve its cursor outcome under the command lane.
	response, err := r.Transport.Execute(ctx, Command{
		CDB: []byte{0x11, code, byte(count >> 16), byte(count >> 8), byte(count), 0}, Direction: NoData, Timeout: r.Timeout,
		validate: controlResponse,
	})
	if err != nil {
		return err
	}
	if err := CheckResponse(response); err != nil {
		return err
	}
	return nil
}

// Locate uses the full 64-bit logical object form with CP set and IMMED clear, without narrowing.
func (r Records) Locate(ctx context.Context, partition byte, object uint64) error {
	// Encode the full logical object without a 32-bit fallback or silent narrowing.
	cdb, err := locateCDB(0x92, uint32(partition), object)
	if err != nil {
		return err
	}
	return r.locate(ctx, cdb)
}

func (r Records) locate(ctx context.Context, cdb []byte) (err error) {
	defer func() { r.geometryError(err) }()
	// A real LOCATE failure ends cursor use rather than attempting speculative recovery.
	response, err := r.Transport.Execute(ctx, Command{CDB: cdb, Direction: NoData, Timeout: r.Timeout, validate: controlResponse})
	if err != nil {
		return err
	}
	if err := CheckResponse(response); err != nil {
		return err
	}
	return nil
}

func (r Records) geometryError(err error) {
	// Lost/unresolved source state withdraws estimates; pure cancellation alone does not claim physical mutation.
	if err != nil && !onlyCancellation(err) && r.invalidateGeometry != nil {
		r.invalidateGeometry()
	}
}

func onlyCancellation(err error) bool {
	// Only cancellation leaves a known geometry observation intact; mixed physical failures invalidate it.
	if err == context.Canceled || err == context.DeadlineExceeded {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !onlyCancellation(cause) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return onlyCancellation(wrapped.Unwrap())
	}
	return false
}
