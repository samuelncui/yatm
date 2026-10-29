package media

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/tape"
	"google.golang.org/protobuf/proto"
)

// Go archive/tar bounds each special-header payload to 1 MiB. Apply the same bound
// to the complete encoded header chain before emission so our writer is readable.
const (
	tarMaxHeaderBytes = (1 << 20) + 1024
	// The accepted path bound remains independent of ACP's generic attribute limits.
	tarMaxPathBytes = 4 << 20
)

type tarWriteSession struct {
	device           TarRecordDevice
	options          TarSessionOptions
	writer           *tar.Writer
	buffer           []byte
	ctx              context.Context
	offset, accepted uint64
	active           *tarContentWriter
	stop, unusable   error
	finalized        bool
	bufferedHeader   *uint64
	acceptedHeader   *uint64
	positionStarted  bool
	position         *tape.Position
	objectLimit      uint64
	acceptedRecords  uint64
	capacity         *tarCapacityBudget
	admissionStop    error
}

// NewTarWriteSession wraps an already prepared, exclusively owned Tape file.
func NewTarWriteSession(device TarRecordDevice, options TarSessionOptions) (WriteSession, error) {
	// Validate the prepared stream and capacity policy before admitting archive bytes.
	if err := checkTarOptions(device, options); err != nil {
		return nil, err
	}
	if options.TapeFile == 0 {
		return nil, errors.New("tar write requires a content Tape file")
	}
	if options.Capacity != nil && options.Capacity.Observe != nil {
		if _, ok := device.(tarCapacityWriter); !ok {
			return nil, errors.New("live tar capacity requires actual WRITE issue observations")
		}
	}
	capacity, err := newTarCapacityBudget(options.Capacity, options.Mode)
	if err != nil {
		return nil, err
	}
	s := &tarWriteSession{
		device: device, options: options,
		buffer: make([]byte, 0, options.Mode.MaximumBytes), stop: options.Stop, capacity: capacity,
	}
	s.writer = tar.NewWriter(s)
	if options.Stop == nil {
		s.capacity.start()
	}
	return s, nil
}

func (s *tarWriteSession) Capabilities() Capabilities {
	return Capabilities{Read: AccessSequential, Write: AccessSequential}
}
func (s *tarWriteSession) Media() *Descriptor { return s.options.Media }

func (s *tarWriteSession) Target(relative string, _ int64) (TargetRef, error) {
	if err := entity.ValidateRelativePath(relative); err != nil {
		return TargetRef{}, err
	}
	// The feeder constructs references concurrently; dynamic admission belongs to ordered Open.
	if s.options.Stop != nil {
		return TargetRef{}, s.options.Stop
	}
	if s.finalized {
		return TargetRef{}, errors.New("tar write admission is closed")
	}
	return TargetRef{Content: &tarContentTarget{session: s, path: tarPrefix(s.options.TapeFile) + "/" + relative}}, nil
}

type tarContentTarget struct {
	session *tarWriteSession
	path    string
	storage *entity.StoragePosition
}

func (t *tarContentTarget) storagePosition() *entity.StoragePosition {
	if t.storage == nil {
		return nil
	}
	return proto.Clone(t.storage).(*entity.StoragePosition)
}

func (t *tarContentTarget) Open(ctx context.Context, metadata acp.ContentMetadata) (acp.ContentWriter, error) {
	// Encode a bounded header with the standard writer before admitting the member.
	s := t.session
	if s.stop != nil {
		return nil, s.stop
	}
	if s.admissionStop != nil {
		return nil, s.admissionStop
	}
	if err := s.capacity.err(); err != nil {
		s.stop, s.unusable = err, err
		return nil, err
	}
	if s.finalized || s.active != nil {
		return nil, errors.New("tar target lane is unavailable")
	}
	if err := ctx.Err(); err != nil {
		s.stop = err
		return nil, err
	}
	header, err := tarHeader(t.path, metadata)
	if err != nil {
		return nil, err
	}
	var encoded bytes.Buffer
	probe := tar.NewWriter(&encoded)
	if err := probe.WriteHeader(header); err != nil {
		return nil, fmt.Errorf("encode tar member header failed, %w", err)
	}
	if encoded.Len() > tarMaxHeaderBytes {
		return nil, errors.New("tar member header exceeds supported metadata envelope")
	}

	// Header/payload admission is serialized; no per-member device flush is performed.
	s.ctx = ctx
	if err := s.beginPosition(ctx); err != nil {
		s.stop, s.unusable = err, err
		return nil, err
	}
	if s.stop != nil {
		return nil, s.stop
	}

	// Debit observed content and the already encoded PAX chain once, before its first byte.
	memberBytes := uint64(encoded.Len()) + uint64((header.Size+511)/512*512)
	if err := s.capacity.reserve(memberBytes); err != nil {
		if errors.Is(err, ErrTargetNoSpace) {
			s.admissionStop = errors.Join(err, ErrCapacityBoundary)
			return nil, s.admissionStop
		}
		if errors.Is(err, tape.ErrUncertain) {
			s.stop, s.unusable = err, err
		}
		return nil, err
	}
	start := s.offset
	position := s.currentPosition()
	if err := s.writer.WriteHeader(header); err != nil {
		s.stop = err
		return nil, err
	}
	w := &tarContentWriter{session: s, target: t, header: start, headerPosition: position, size: header.Size}
	s.active = w
	return w, nil
}

func tarHeader(name string, metadata acp.ContentMetadata) (*tar.Header, error) {
	// Keep portable ordinary-file metadata and validate its PAX representation before emission.
	if metadata.Info == nil || !metadata.Info.Mode().IsRegular() {
		return nil, errors.New("tar member requires regular file metadata")
	}
	if metadata.Info.Size() < 0 || metadata.Info.Size() > math.MaxInt64-511 {
		return nil, errors.New("tar member size exceeds the supported envelope")
	}
	if len(name) > tarMaxPathBytes || !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return nil, errors.New("unsupported tar member path")
	}
	mode := metadata.Info.Mode()
	h := &tar.Header{
		Name: name, Typeflag: tar.TypeReg, Format: tar.FormatPAX, Size: metadata.Info.Size(),
		Mode: int64(mode.Perm()), ModTime: metadata.Info.ModTime(), PAXRecords: make(map[string]string),
	}
	// Standard tar permission bits encode Go's special mode flags separately from Perm.
	if mode&os.ModeSetuid != 0 {
		h.Mode |= 04000
	}
	if mode&os.ModeSetgid != 0 {
		h.Mode |= 02000
	}
	if mode&os.ModeSticky != 0 {
		h.Mode |= 01000
	}

	// ACP selects ordinary attributes; the codec owns only representable PAX keys.
	for key, value := range metadata.Xattrs {
		if err := checkTarXattrName(key); err != nil {
			return nil, err
		}
		h.PAXRecords["SCHILY.xattr."+key] = string(value)
	}
	return h, nil
}

func checkTarXattrName(name string) error {
	if len(name) == 0 || strings.ContainsAny(name, "\x00\n=") || !utf8.ValidString(name) {
		return fmt.Errorf("unsupported tar xattr name, name=%q", name)
	}
	return nil
}

func (s *tarWriteSession) Write(data []byte) (int, error) {
	// ACP chunks share physical records across member boundaries.
	used := 0
	for len(data) != 0 {
		if s.stop != nil {
			return used, s.stop
		}
		if err := s.ctx.Err(); err != nil {
			s.stop = err
			return used, err
		}
		n := min(len(data), cap(s.buffer)-len(s.buffer))
		if s.offset > math.MaxUint64-uint64(n) {
			s.stop = errors.New("tar stream byte offset overflow")
			return used, s.stop
		}
		s.buffer = append(s.buffer, data[:n]...)
		s.offset += uint64(n)
		used += n
		data = data[n:]
		if len(s.buffer) == cap(s.buffer) {
			if err := s.writeRecord(); err != nil {
				return used, err
			}
		}
	}
	return used, nil
}

func (s *tarWriteSession) writeRecord() error {
	// Keep host-known accepted byte and record counters bounded before admitting another command.
	if s.accepted > math.MaxUint64-uint64(len(s.buffer)) || s.acceptedRecords == math.MaxUint64 {
		s.unusable = errors.New("tar accepted record counter overflow")
		s.stop = s.unusable
		return s.stop
	}

	// Optional address exhaustion never fabricates a wrapped locator or blocks baseline writing.
	if s.position != nil {
		available := s.objectLimit - s.position.Object
		if s.acceptedRecords >= available || available-s.acceptedRecords < 2 {
			// Optional seek loss omits all provisional anchors; sequential acceptance remains valid.
			s.position = nil
		}
	}

	// Only a known full record advances the contiguous physical acceptance boundary.
	recordBytes := len(s.buffer)
	var result tape.RecordResult
	var err error
	observed := s.capacity != nil && s.capacity.options.Observe != nil
	if observed {
		result, err = s.device.(tarCapacityWriter).WriteObserved(s.ctx, s.buffer, s.capacity.err)
	} else {
		result, err = s.device.Write(s.ctx, s.buffer)
	}
	if !result.AcceptedKnown || result.Bytes != 0 && result.Bytes != len(s.buffer) {
		s.unusable = errors.Join(err, errors.New("tar record acceptance is unknown or partial"))
		s.stop = s.unusable
		return s.stop
	}
	if result.Bytes == len(s.buffer) {
		s.accepted += uint64(result.Bytes)
		s.acceptedRecords++
		// This full record covers earlier successful Finishes, never the active unfinished member.
		if s.bufferedHeader != nil {
			s.acceptedHeader, s.bufferedHeader = s.bufferedHeader, nil
		}
		if observed {
			if capacityErr := s.capacity.returned(uint64(result.Bytes)); capacityErr != nil {
				err = errors.Join(err, capacityErr)
			}
		}
	}
	s.buffer = s.buffer[:0]

	// Only an exclusive, qualified capacity warning can retain these accepted records for completion.
	if result.EarlyWarning && errors.Is(err, tape.ErrEarlyWarning) {
		remaining := int64(recordBytes - result.Bytes)
		if !tape.WriteWarning(err, remaining, s.options.Mode) || !tarCapacityStop(err, s.options.Mode) {
			s.unusable, s.stop = err, err
			return err
		}
		s.stop = errors.Join(ErrTargetNoSpace, err)
		return s.stop
	}
	if err != nil {
		s.stop, s.unusable = err, err
		return err
	}
	if result.Bytes == 0 {
		s.stop = io.ErrShortWrite
		s.unusable = s.stop
		return s.stop
	}
	return nil
}

type tarContentWriter struct {
	session        *tarWriteSession
	target         *tarContentTarget
	header         uint64
	headerPosition *entity.TapeBytePosition
	size, written  int64
	finished       bool
}

func (w *tarContentWriter) Write(data []byte) (int, error) {
	if w.finished {
		return 0, errors.New("tar member is finished")
	}
	n, err := w.session.writer.Write(data)
	w.written += int64(n)
	return n, err
}

func (w *tarContentWriter) Finish(cause error) error {
	// Failure stops the archive without synthesizing missing payload or submitting a suffix.
	if w.finished {
		return errors.New("tar member Finish called twice")
	}
	w.finished = true
	s := w.session
	s.active = nil
	if cause != nil || s.stop != nil || w.written != w.size {
		if cause == nil && s.stop == nil {
			cause = io.ErrUnexpectedEOF
		}
		s.stop = errors.Join(s.stop, cause)
		return s.stop
	}
	if err := s.writer.Flush(); err != nil {
		s.stop = err
		return err
	}

	// Successful framing exposes a positioning snapshot to the existing ACP result owner.
	storage := tarStorage(s.options.TapeFile, w.header)
	if end := s.currentPosition(); w.headerPosition != nil && end != nil {
		storage.Metadata.GetTar().Header, storage.Metadata.GetTar().End = w.headerPosition, end
	}
	w.target.storage = storage

	// Nil distinguishes no completed member from the valid first header at byte zero.
	if s.offset <= s.accepted {
		s.acceptedHeader = proto.Uint64(w.header)
	} else {
		s.bufferedHeader = proto.Uint64(w.header)
	}
	return nil
}

func (s *tarWriteSession) Finalize(ctx context.Context, cause error) (*WriteResult, error) {
	// Settle issued capacity work before deciding whether this stream can complete.
	if s.finalized {
		return nil, errors.Join(ErrFinalizeUnusable, errors.New("tar session finalized twice"))
	}
	s.finalized = true
	s.ctx = ctx
	if err := s.capacity.stop(); err != nil {
		s.stop, s.unusable = errors.Join(s.stop, err), errors.Join(s.unusable, err)
	}
	if s.active != nil {
		s.stop = errors.Join(s.stop, io.ErrUnexpectedEOF)
	}
	if cause != nil && !tarCapacityStop(cause, s.options.Mode) {
		s.unusable = errors.Join(s.unusable, cause)
	}
	if s.stop != nil && !tarCapacityStop(s.stop, s.options.Mode) {
		s.unusable = errors.Join(s.unusable, s.stop)
	}

	// Interrupted streams have no publishable result; cleanup must not append a rescue boundary.
	if s.unusable != nil {
		s.buffer = nil
		return nil, errors.Join(ErrFinalizeUnusable, s.unusable, s.options.Cleanup(ctx))
	}

	// Per-file source-open failures never enter this cause; only a healthy stream flushes its suffix.
	if s.stop == nil {
		if err := s.beginPosition(ctx); err != nil {
			s.stop, s.unusable = err, err
		}
		if s.stop == nil {
			if err := s.writer.Close(); err != nil {
				s.stop = err
				if !tarCapacityStop(err, s.options.Mode) {
					s.unusable = err
				}
			}
		}
		if s.stop == nil && len(s.buffer) != 0 {
			if s.options.Mode.FixedBytes != 0 {
				s.buffer = append(s.buffer, make([]byte, cap(s.buffer)-len(s.buffer))...)
			}
			_ = s.writeRecord()
		}
	}
	s.buffer = nil

	// Physical completion and preparation cleanup are independent mandatory publication gates.
	var completionErr error
	if s.unusable == nil {
		if s.options.Complete != nil {
			completionErr = s.options.Complete(ctx)
		} else {
			completionErr = s.device.Filemarks(ctx, 1)
		}
		if completionErr == tape.ErrEarlyWarning {
			s.stop = errors.Join(s.stop, ErrTargetNoSpace, completionErr)
			completionErr = nil
		}
	}
	if s.unusable == nil && completionErr == nil {
		completionErr = s.checkCompletedPosition(ctx)
	}
	cleanup := s.options.Cleanup
	if s.options.Finish != nil && errors.Join(s.unusable, completionErr) == nil {
		cleanup = s.options.Finish
	}
	cleanupErr := cleanup(ctx)
	if err := errors.Join(s.unusable, completionErr, cleanupErr); err != nil {
		return nil, errors.Join(ErrFinalizeUnusable, err)
	}

	// Freeze only completion and record facts; Archive already retains every member's staged position.
	accepted, records, file := s.accepted, s.acceptedRecords, s.options.TapeFile
	cutoff, position, recordBytes := s.acceptedHeader, s.position, uint64(s.options.Mode.MaximumBytes)
	stop := s.stop
	if s.admissionStop != nil {
		stop = errors.Join(stop, s.admissionStop)
	}
	return &WriteResult{Media: s.Media(), PathPrefix: tarPrefix(file), Stop: stop,
		finalizeStorage: func(staged *entity.StoragePosition) (*entity.StoragePosition, error) {
			// Missing or inconsistent facts cannot be hidden as an ordinary incomplete tail.
			if staged == nil || staged.Metadata.GetTar() == nil {
				return nil, errors.New("tar staged member storage is missing")
			}
			locator := staged.Metadata.GetTar()
			if err := entity.ValidateTarMetadata(locator, uint32(recordBytes)); err != nil {
				return nil, err
			}
			if locator.TapeFile != file || !bytes.Equal(staged.Order, entity.TarStorageOrder(locator)) {
				return nil, errors.New("tar staged member storage differs from the completed archive")
			}
			if cutoff == nil || locator.HeaderOffsetBytes > *cutoff {
				return nil, nil
			}

			// Completion may withdraw optional anchors; real final-record length normalizes its exclusive end.
			final := proto.Clone(staged).(*entity.StoragePosition)
			locator = final.Metadata.GetTar()
			if position == nil {
				locator.Header, locator.End = nil, nil
			} else if end := locator.End; end != nil && records != 0 && accepted%recordBytes != 0 &&
				end.LogicalObject == position.Object+records-1 && uint64(end.OffsetBytes) == accepted%recordBytes {
				locator.End = &entity.TapeBytePosition{Partition: position.Partition, LogicalObject: position.Object + records}
			}
			return final, nil
		}}, nil
}

func tarCapacityStop(err error, mode tape.RecordMode) bool {
	// Only the known capacity/EW tree permits completion of accepted members.
	if err == ErrTargetNoSpace || err == acp.ErrTargetNoSpace || err == ErrCapacityBoundary || err == tape.ErrEarlyWarning {
		return true
	}
	if failure, ok := err.(*tape.CommandError); ok {
		return tape.WriteWarning(failure, int64(failure.Response.Residual), mode)
	}

	// ACP and Archive preserve independent failures in either standard joins or multierror batches.
	var causes []error
	switch failure := err.(type) {
	case interface{ WrappedErrors() []error }:
		causes = failure.WrappedErrors()
	case interface{ Unwrap() []error }:
		causes = failure.Unwrap()
	case interface{ Unwrap() error }:
		return tarCapacityStop(failure.Unwrap(), mode)
	default:
		return false
	}
	if len(causes) == 0 {
		return false
	}
	for _, cause := range causes {
		if !tarCapacityStop(cause, mode) {
			return false
		}
	}
	return true
}

// beginPosition freezes one qualified host-next anchor before any content or trailer emission.
func (s *tarWriteSession) beginPosition(ctx context.Context) error {
	if s.positionStarted || s.options.Navigator == nil {
		return nil
	}
	s.positionStarted = true
	position, err := s.options.Navigator.Position(ctx)
	if tape.CleanUnsupported(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read tar start position failed, %w", err)
	}
	if position.Beginning || position.FileKnown && position.File != s.options.TapeFile {
		return errors.New("tar start position contradicts the prepared Tape file")
	}
	if position.BeyondEarlyWarning {
		s.stop = errors.Join(ErrTargetNoSpace, tape.ErrEarlyWarning)
	}
	if !position.ObjectKnown {
		return nil
	}
	if position.Partition != 0 {
		return errors.New("tar start position has an unsupported data partition")
	}

	// A short-only provider never wraps its address; reserve the terminating filemark too.
	s.objectLimit = math.MaxUint64
	if s.options.Navigator.AddressBits() == 32 {
		s.objectLimit = math.MaxUint32
	}
	if position.Object >= s.objectLimit || s.options.TapeFile == math.MaxUint64 {
		// Unrepresentable optional addresses do not prevent physically proven sequential writes.
		return nil
	}
	s.position = &position
	return nil
}

// currentPosition attaches a provisional boundary in the single current assembled record.
func (s *tarWriteSession) currentPosition() *entity.TapeBytePosition {
	if s.position == nil {
		return nil
	}
	return &entity.TapeBytePosition{Partition: s.position.Partition,
		LogicalObject: s.position.Object + s.acceptedRecords, OffsetBytes: uint32(len(s.buffer))}
}

// checkCompletedPosition checks the synchronized filemark once before optional anchors publish.
func (s *tarWriteSession) checkCompletedPosition(ctx context.Context) error {
	if s.position == nil {
		return nil
	}
	position, err := s.options.Navigator.Position(ctx)
	if tape.CleanUnsupported(err) {
		s.position = nil
		return nil
	}
	if err != nil {
		return fmt.Errorf("read tar completed position failed, %w", err)
	}
	if position.Beginning || position.FileKnown && position.File != s.options.TapeFile+1 {
		return errors.New("tar completed position contradicts the completed Tape file")
	}
	if !position.ObjectKnown {
		s.position = nil
		return nil
	}
	if position.Partition != s.position.Partition || position.Object != s.position.Object+s.acceptedRecords+1 {
		return errors.New("tar completed position contradicts accepted records and filemark")
	}
	return nil
}
