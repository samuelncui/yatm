package media

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"strconv"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/readplan"
	"github.com/samuelncui/yatm/internal/media/tape"
	"google.golang.org/protobuf/proto"
)

type tarRecordReader struct {
	device                            TarRecordDevice
	ctx                               context.Context
	buffer, pending                   []byte
	offset, recordStart               uint64
	length                            int
	object, nextObject, maximumObject uint64
	known, nextKnown                  bool
	boundary                          tape.RecordResult
	err                               error
}

func (r *tarRecordReader) readRecord() error {
	// Keep the complete current record for later members, including headers already consumed.
	result, err := r.device.Read(r.ctx, r.buffer)
	if err != nil {
		// Tag known device-wide failures; medium/payload errors remain the first member observation.
		r.err = err
		switch tape.Reason(err) {
		case entity.DeviceReason_DEVICE_REASON_NO_MEDIUM, entity.DeviceReason_DEVICE_REASON_NOT_READY,
			entity.DeviceReason_DEVICE_REASON_BUSY, entity.DeviceReason_DEVICE_REASON_MISSING_TOOL,
			entity.DeviceReason_DEVICE_REASON_OWNERSHIP_UNCERTAIN, entity.DeviceReason_DEVICE_REASON_PERMISSION,
			entity.DeviceReason_DEVICE_REASON_MALFORMED_RESPONSE, entity.DeviceReason_DEVICE_REASON_UNSUPPORTED:
			r.err = errors.Join(ErrReadUnavailable, err)
		}
		return r.err
	}
	if !result.AcceptedKnown || result.Bytes < 0 || result.Bytes > len(r.buffer) {
		r.err = errors.Join(ErrReadUnavailable, errors.New("tar read record byte count is invalid"))
		return r.err
	}
	if result.Filemark || result.EOD {
		if result.Bytes != 0 {
			r.err = errors.Join(ErrReadUnavailable, errors.New("tar boundary contains data"))
			return r.err
		}
		r.boundary = result
		if result.Filemark {
			r.advanceObject()
		}
		return io.EOF
	}
	if result.Bytes == 0 {
		r.err = errors.Join(ErrReadUnavailable, io.ErrNoProgress)
		return r.err
	}
	r.recordStart, r.length = r.offset, result.Bytes
	r.object, r.known = r.nextObject, r.nextKnown
	r.advanceObject()
	r.pending = r.buffer[:result.Bytes]
	return nil
}

func (r *tarRecordReader) advanceObject() {
	// Address exhaustion omits optional anchors without fabricating a wrapped position.
	if !r.nextKnown {
		return
	}
	if r.nextObject == r.maximumObject {
		r.nextKnown = false
		return
	}
	r.nextObject++
}

func (r *tarRecordReader) position() *entity.TapeBytePosition {
	// Normalize the exclusive byte boundary using the actual record length, never nominal division.
	if r.offset < r.recordStart {
		return nil
	}
	if r.length == 0 || r.offset-r.recordStart == uint64(r.length) {
		if !r.nextKnown {
			return nil
		}
		return &entity.TapeBytePosition{LogicalObject: r.nextObject}
	}
	if !r.known || r.offset < r.recordStart || r.offset-r.recordStart >= uint64(r.length) {
		return nil
	}
	return &entity.TapeBytePosition{LogicalObject: r.object, OffsetBytes: uint32(r.offset - r.recordStart)}
}

func (r *tarRecordReader) Read(data []byte) (int, error) {
	// A filemark/EOD is a physical boundary; actual read errors retain their original cause.
	if len(data) == 0 {
		return 0, nil
	}
	if len(r.pending) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		if r.boundary.Filemark || r.boundary.EOD {
			return 0, io.EOF
		}
		if err := r.readRecord(); err != nil {
			return 0, err
		}
	}

	// Advance only consumed bytes; retained suffixes do not advance the device object again.
	n := copy(data, r.pending)
	if r.offset > math.MaxUint64-uint64(n) {
		r.err = errors.New("tar read byte offset overflow")
		return 0, r.err
	}
	r.pending = r.pending[n:]
	r.offset += uint64(n)
	return n, nil
}

type tarReadMember struct {
	reader     *tar.Reader
	header     *tar.Header
	start, end uint64
	xattrs     map[string][]byte
	position   *entity.TapeBytePosition
}

func nextTarMember(r *tarRecordReader, file uint64) (*tarReadMember, error) {
	// Guard framing and total local-header allocation; archive/tar owns checksum/PAX parsing.
	start, position := r.offset, r.position()
	var chain bytes.Buffer
	for {
		var block [512]byte
		if _, err := io.ReadFull(r, block[:]); err != nil {
			return nil, err
		}
		if bytes.Equal(block[:], make([]byte, 512)) {
			if chain.Len() != 0 {
				return nil, errors.New("tar local PAX header has no member")
			}
			if _, err := io.ReadFull(r, block[:]); err != nil {
				return nil, err
			}
			if !bytes.Equal(block[:], make([]byte, 512)) {
				return nil, errors.New("tar terminator second block is not zero")
			}
			return nil, io.EOF
		}
		kind := block[156]
		if kind != tar.TypeXHeader && kind != tar.TypeReg && kind != tar.TypeRegA {
			return nil, fmt.Errorf("unsupported tar member type, type=%d", kind)
		}
		chain.Write(block[:])
		if kind != tar.TypeXHeader {
			break
		}
		size, err := tarExtensionSize(block[124:136])
		if err != nil {
			return nil, err
		}
		padded := (size + 511) / 512 * 512
		if padded > tarMaxHeaderBytes-chain.Len()-512 {
			return nil, errors.New("tar local PAX chain exceeds metadata envelope")
		}
		payload := make([]byte, padded)
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, err
		}
		if !tarZeroBytes(payload[size:]) {
			return nil, errors.New("tar PAX padding is not zero")
		}
		chain.Write(payload)
	}

	// The standard reader interprets the bounded chain and streams the original record payload.
	reader := tar.NewReader(io.MultiReader(bytes.NewReader(chain.Bytes()), r))
	header, err := reader.Next()
	if err != nil {
		return nil, fmt.Errorf("parse tar header failed, %w", err)
	}
	if header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > math.MaxInt64-511 {
		return nil, errors.New("unsupported tar regular member size/type")
	}
	if file == 0 {
		if header.Name != "YATM/identity.json" && header.Name != "YATM/README.txt" {
			return nil, errors.New("unsupported tar identity member")
		}
	} else if err := checkTarPath(header.Name, file); err != nil {
		return nil, err
	}

	// Decode raw PAX attributes; ACP owns their ordinary selection and generic limits.
	xattrs := make(map[string][]byte)
	for key, value := range header.PAXRecords {
		if strings.HasPrefix(key, "GNU.sparse.") {
			return nil, errors.New("tar sparse extensions are unsupported")
		}
		if !strings.HasPrefix(key, "SCHILY.xattr.") {
			continue
		}
		name := strings.TrimPrefix(key, "SCHILY.xattr.")
		if err := checkTarXattrName(name); err != nil {
			return nil, err
		}
		xattrs[name] = []byte(value)
	}

	// The exclusive member end includes padding regardless of its physical record boundaries.
	padded := uint64((header.Size + 511) / 512 * 512)
	if r.offset > math.MaxUint64-padded {
		return nil, errors.New("tar member byte offset overflow")
	}
	return &tarReadMember{reader: reader, header: header, start: start, end: r.offset + padded, xattrs: xattrs, position: position}, nil
}

func tarExtensionSize(field []byte) (int, error) {
	// This transport envelope reads only the extension length; standard tar validates the header.
	if field[0]&0x80 != 0 {
		return 0, errors.New("unsupported tar extension size encoding")
	}
	size, err := strconv.ParseUint(strings.Trim(string(field), " \x00"), 8, 32)
	if err != nil || size > 1<<20 {
		return 0, errors.New("tar extension size exceeds metadata envelope")
	}
	return int(size), nil
}

func tarZeroBytes(data []byte) bool {
	for _, value := range data {
		if value != 0 {
			return false
		}
	}
	return true
}

func finishTarMember(r *tarRecordReader, member *tarReadMember) error {
	// A successful consumer Close requires complete payload and its observed zero padding.
	if r.offset > member.end {
		return errors.New("tar member cursor exceeds exclusive end")
	}
	if remaining := member.end - r.offset; remaining > 511 {
		return io.ErrUnexpectedEOF
	}
	padding := make([]byte, int(member.end-r.offset))
	if _, err := io.ReadFull(r, padding); err != nil {
		return err
	}
	if !tarZeroBytes(padding) {
		return errors.New("tar member padding is not zero")
	}
	return nil
}

type tarReadSession struct {
	device            TarRecordDevice
	options           TarSessionOptions
	records           *tarRecordReader
	file              uint64
	fileKnown         bool
	active            bool
	finalized         bool
	navigator         TarNavigator
	failed            error
	readOrderDisabled bool
}

// NewTarReadSession wraps a prepared Tape; construction performs no motion or data reads.
func NewTarReadSession(device TarRecordDevice, options TarSessionOptions) (InventorySession, error) {
	// Preparation owns optional facts; detach them from the caller's mutable settings snapshot.
	if err := checkTarOptions(device, options); err != nil {
		return nil, err
	}
	if options.ReadTiming != nil {
		options.ReadTiming = proto.Clone(options.ReadTiming).(*entity.TapeReadTiming)
	}
	if position := options.ReadInitialPosition; position != nil {
		if position.Partition != 0 || position.OffsetBytes != 0 ||
			options.Navigator != nil && options.Navigator.AddressBits() == 32 && position.LogicalObject > math.MaxUint32 {
			return nil, errors.New("tar initial host-next position is invalid")
		}
		options.ReadInitialPosition = proto.Clone(position).(*entity.TapeBytePosition)
	}

	// A successful label read can prove the file independently of optional logical coordinates.
	s := &tarReadSession{device: device, options: options, navigator: options.Navigator,
		file: options.TapeFile, fileKnown: options.ReadInitialFileKnown}
	if options.ReadInitialFileKnown || options.ReadInitialPosition != nil {
		object := uint64(0)
		if options.ReadInitialPosition != nil {
			object = options.ReadInitialPosition.LogicalObject
		}
		s.records = s.recordReader(context.Background(), 0, object, options.ReadInitialPosition != nil)
	}
	return s, nil
}

func (s *tarReadSession) Media() *Descriptor { return s.options.Media }
func (s *tarReadSession) Capabilities() Capabilities {
	return Capabilities{Read: AccessSequential, Write: AccessSequential}
}

func (s *tarReadSession) recordReader(ctx context.Context, offset, object uint64, known bool) *tarRecordReader {
	maximum := uint64(math.MaxUint64)
	if s.navigator != nil && s.navigator.AddressBits() == 32 {
		maximum = math.MaxUint32
	}
	return &tarRecordReader{device: s.device, ctx: ctx, buffer: make([]byte, s.options.Mode.MaximumBytes),
		offset: offset, recordStart: offset, nextObject: object, nextKnown: known, maximumObject: maximum}
}

func (s *tarReadSession) observe(ctx context.Context) (tape.Position, error) {
	// Optional unsupported/unknown observations retain sequential reads; uncertain responses do not.
	if s.navigator == nil {
		return tape.Position{}, nil
	}
	position, err := s.navigator.Position(ctx)
	if tape.CleanUnsupported(err) && ctx.Err() == nil {
		s.navigator = nil
		return tape.Position{}, nil
	}
	if err != nil {
		return tape.Position{}, errors.Join(ErrReadUnavailable, err)
	}
	if s.fileKnown && (position.Beginning || position.FileKnown && position.File != s.file) {
		return tape.Position{}, errors.Join(ErrReadUnavailable, errors.New("tar position differs from navigated Tape file"))
	}
	if !position.ObjectKnown || s.navigator.AddressBits() == 32 && position.Object > math.MaxUint32 {
		s.navigator = nil
		position.ObjectKnown = false
		return position, nil
	}
	if position.Partition != 0 {
		return tape.Position{}, errors.Join(ErrReadUnavailable, errors.New("tar position is not in partition zero"))
	}
	return position, nil
}

func (s *tarReadSession) position(ctx context.Context, file uint64) error {
	// Baseline navigation uses physical filemarks, never catalog estimates of recorded EOD.
	if err := s.device.Rewind(ctx); err != nil {
		return errors.Join(ErrReadUnavailable, err)
	}
	remaining := file
	for remaining != 0 {
		n := min(remaining, uint64(0x7fffff))
		if err := s.device.Space(ctx, 1, int32(n)); err != nil {
			return errors.Join(ErrReadUnavailable, err)
		}
		remaining -= n
	}

	// Selected sequential reads need only the controlled file ordinal, not optional object evidence.
	s.file, s.fileKnown = file, true
	s.records = s.recordReader(ctx, 0, 0, false)
	return nil
}

func (s *tarReadSession) seek(ctx context.Context, locator *entity.TarMetadata) (bool, error) {
	// Both anchors must fit the qualified command width before any optional observation or motion.
	if s.navigator == nil || locator.Header == nil {
		return false, nil
	}
	header, end := locator.Header, locator.End
	if s.navigator.AddressBits() == 32 && (header.LogicalObject > math.MaxUint32 || end.LogicalObject > math.MaxUint32) {
		return false, nil
	}
	if header.Partition != 0 || end.Partition != 0 || locator.HeaderOffsetBytes < uint64(header.OffsetBytes) {
		return false, errors.New("tar physical header position is invalid")
	}
	start := locator.HeaderOffsetBytes - uint64(header.OffsetBytes)

	// A retained record can serve an earlier header without changing the physical device cursor.
	if s.records != nil && s.file == locator.TapeFile && s.records.known && s.records.object == header.LogicalObject {
		if s.records.recordStart != start || int(header.OffsetBytes) >= s.records.length {
			return false, errors.New("tar header differs from retained record")
		}
		s.records.ctx, s.records.offset = ctx, locator.HeaderOffsetBytes
		s.records.pending = s.records.buffer[header.OffsetBytes:s.records.length]
		return true, nil
	}

	// The next unread object can already contain the requested header after a shared-record read.
	if s.records != nil && s.file == locator.TapeFile && s.records.nextKnown && s.records.nextObject == header.LogicalObject {
		if s.records.recordStart > math.MaxUint64-uint64(s.records.length) || start != s.records.recordStart+uint64(s.records.length) {
			return false, errors.New("tar header differs from next record boundary")
		}
		return true, s.loadHeader(ctx, locator, start)
	}

	// Observe only an unknown lane; controlled reads and motion already prove the next device object.
	if s.records == nil || !s.records.nextKnown {
		position, err := s.observe(ctx)
		if err != nil || !position.ObjectKnown {
			return false, err
		}
	}
	if err := s.navigator.Locate(ctx, 0, header.LogicalObject); err != nil {
		return false, errors.Join(ErrReadUnavailable, err)
	}
	return true, s.loadHeader(ctx, locator, start)
}

func (s *tarReadSession) loadHeader(ctx context.Context, locator *entity.TarMetadata, start uint64) error {
	// Read the whole containing record before accepting the frozen in-record header offset.
	s.file, s.fileKnown = locator.TapeFile, true
	s.records = s.recordReader(ctx, start, locator.Header.LogicalObject, true)
	if err := s.records.readRecord(); err != nil {
		return err
	}
	if int(locator.Header.OffsetBytes) >= s.records.length {
		return errors.New("tar header exceeds actual record length")
	}
	s.records.offset = locator.HeaderOffsetBytes
	s.records.pending = s.records.buffer[locator.Header.OffsetBytes:s.records.length]
	return nil
}

func (s *tarReadSession) prepareRead(ctx context.Context, locator *entity.TarMetadata) (bool, *readplan.State, error) {
	// Runtime uses exactly the action evaluated by the planner, when that action has a complete quote.
	interval, anchored := s.readRange(locator)
	transition, known := s.evaluateRead(s.readState(), interval)
	if !anchored || !known {
		positioned, err := s.seek(ctx, locator)
		return positioned, nil, err
	}
	start := locator.HeaderOffsetBytes - uint64(locator.Header.OffsetBytes)
	switch transition.Method {
	case readplan.MethodCache:
		if s.records == nil || s.records.recordStart != start || int(locator.Header.OffsetBytes) >= s.records.length {
			return false, nil, errors.New("tar header differs from retained record")
		}
		s.records.ctx, s.records.offset = ctx, locator.HeaderOffsetBytes
		s.records.pending = s.records.buffer[locator.Header.OffsetBytes:s.records.length]
	case readplan.MethodContinue:
		if err := s.continueRead(ctx, locator.TapeFile); err != nil {
			return false, nil, err
		}
	case readplan.MethodSeek:
		if err := s.navigator.Locate(ctx, 0, locator.Header.LogicalObject); err != nil {
			return false, nil, errors.Join(ErrReadUnavailable, err)
		}
		if err := s.loadHeader(ctx, locator, start); err != nil {
			return false, nil, err
		}
	default:
		return false, nil, errors.New("tar read action is unavailable")
	}
	return true, &transition.Next, nil
}

func (s *tarReadSession) continueRead(ctx context.Context, file uint64) error {
	// Cross APPEND files only after parsing real members, zero trailer padding and a real FILEMARK.
	ctx = tape.WithReadDiscard(ctx)
	s.records.ctx = ctx
	for s.file < file {
		for {
			member, err := nextTarMember(s.records, s.file)
			if err == nil {
				_, err = io.Copy(io.Discard, member.reader)
				if err == nil {
					err = finishTarMember(s.records, member)
				}
				if err == nil {
					continue
				}
			}
			if s.records.err != nil {
				return s.records.err
			}
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				return err
			}
			break
		}
		if s.records.err != nil {
			return s.records.err
		}
		if !s.records.boundary.Filemark && !s.records.boundary.EOD {
			if err := s.consumeTrailer(); err != nil {
				return err
			}
		}
		if !s.records.boundary.Filemark {
			return errors.New("tar read-through reached EOD before the selected Tape file")
		}
		s.file++
		s.records = s.recordReader(ctx, 0, s.records.nextObject, s.records.nextKnown)
	}
	return nil
}

func (s *tarReadSession) Source(request ReadRequest) (SourceRef, error) {
	if s.finalized {
		return SourceRef{}, errors.New("tar read session is finalized")
	}
	if _, err := tarLocator(request, uint32(s.options.Mode.MaximumBytes)); err != nil {
		return SourceRef{}, err
	}
	return SourceRef{Content: &tarContentSource{session: s, request: request}}, nil
}

type tarContentSource struct {
	session *tarReadSession
	request ReadRequest
}

func (source *tarContentSource) Open(ctx context.Context) (file fs.File, openErr error) {
	// A selected read reuses the forward cursor and proves every skipped member boundary.
	s := source.session
	if s.failed != nil {
		return nil, errors.Join(ErrReadUnavailable, s.failed)
	}
	if s.active || s.finalized {
		return nil, errors.New("tar source lane is unavailable")
	}
	defer func() {
		if openErr != nil {
			s.failed = openErr
		}
	}()

	// Resolve the selected member against the current physical cursor and frozen locator.
	locator, err := tarLocator(source.request, uint32(s.options.Mode.MaximumBytes))
	if err != nil {
		return nil, err
	}
	positioned, prediction, err := s.prepareRead(ctx, locator)
	if err != nil {
		return nil, err
	}
	if !positioned && (s.records == nil || !s.fileKnown || s.file != locator.TapeFile || s.records.offset > locator.HeaderOffsetBytes) {
		if err := s.position(ctx, locator.TapeFile); err != nil {
			return nil, err
		}
	}

	// Skipped members must settle their real padding before the selected source can open.
	discardCtx := tape.WithReadDiscard(ctx)
	for {
		s.records.ctx = ctx
		member, err := nextTarMember(s.records, locator.TapeFile)
		if err != nil {
			return nil, err
		}
		if member.start == locator.HeaderOffsetBytes {
			if member.header.Name != source.request.Path {
				return nil, errors.New("tar locator/header path mismatch")
			}
			if member.position != nil && locator.Header != nil && !proto.Equal(member.position, locator.Header) {
				return nil, errors.New("tar member physical header differs from locator")
			}
			s.active = true
			reader := &tarContentReader{session: s, member: member, prediction: prediction, locator: locator}
			if positioned || member.position != nil {
				reader.end = locator.End
			}
			return reader, nil
		}
		if member.start > locator.HeaderOffsetBytes {
			return nil, errors.New("tar locator is not a header-chain boundary")
		}
		s.records.ctx = discardCtx
		if _, err := io.Copy(io.Discard, member.reader); err != nil {
			return nil, err
		}
		if err := finishTarMember(s.records, member); err != nil {
			return nil, err
		}
	}
}

type tarContentReader struct {
	session    *tarReadSession
	member     *tarReadMember
	read       int64
	closed     bool
	end        *entity.TapeBytePosition
	prediction *readplan.State
	locator    *entity.TarMetadata
}

func (r *tarContentReader) Stat() (fs.FileInfo, error) { return r.member.header.FileInfo(), nil }

func (r *tarContentReader) Xattrs() (map[string][]byte, error) { return r.member.xattrs, nil }

func (r *tarContentReader) Read(data []byte) (int, error) {
	if r.closed {
		return 0, errors.New("tar member reader is closed")
	}
	if r.session.failed != nil {
		return 0, r.session.failed
	}
	n, err := r.member.reader.Read(data)
	r.read += int64(n)
	if err != nil && !errors.Is(err, io.EOF) {
		r.session.failed = err
	}
	return n, err
}

func (r *tarContentReader) Close() error {
	if r.closed {
		return errors.New("tar member Close called twice")
	}
	// Failed payload/padding never admits another source or acknowledges an estimated end.
	r.closed = true
	r.session.active = false
	if r.session.failed != nil {
		return r.session.failed
	}
	if r.read != r.member.header.Size {
		r.session.failed = io.ErrUnexpectedEOF
		return r.session.failed
	}
	if err := finishTarMember(r.session.records, r.member); err != nil {
		r.session.failed = err
		return err
	}

	// Compare the exclusive member byte cursor, which may precede the next unread device object.
	if r.end != nil {
		position := r.session.records.position()
		if position == nil || position.Partition != r.end.Partition || position.LogicalObject != r.end.LogicalObject || position.OffsetBytes != r.end.OffsetBytes {
			r.session.failed = errors.New("tar member physical end differs from locator")
			return r.session.failed
		}
	}

	// A safe difference from predicted record consumption withdraws only later cost estimates.
	r.session.finishReadOrder(r.end, r.prediction)
	if expected, known := r.session.readMemberEnd(r.locator); known && expected != r.member.end {
		r.session.readOrderDisabled = true
	}
	return nil
}

func (s *tarReadSession) Finalize(ctx context.Context, cause error) error {
	if s.finalized {
		return errors.New("tar read session finalized twice")
	}
	s.finalized = true
	if cause == nil && s.failed == nil && !s.active && s.options.Finish != nil {
		return s.options.Finish(ctx)
	}
	return s.options.Cleanup(ctx)
}

func (s *tarReadSession) WalkInventory(ctx context.Context, emit func(*InventoryEntry) error) (inventoryErr error) {
	// A complete fresh inventory starts after the identity archive and includes every physical file.
	if s.failed != nil {
		return errors.Join(ErrReadUnavailable, s.failed)
	}
	defer func() {
		if inventoryErr != nil {
			s.failed = inventoryErr
		}
	}()
	if s.active || s.finalized {
		return errors.New("tar inventory lane is unavailable")
	}
	if err := s.position(ctx, 1); err != nil {
		return err
	}

	// Fresh inventory alone observes optional coordinates for newly emitted physical anchors.
	position, err := s.observe(ctx)
	if err != nil {
		return err
	}
	s.records.nextObject, s.records.nextKnown = position.Object, position.ObjectKnown
	for {
		member, err := nextTarMember(s.records, s.file)
		if err == nil {
			_, err = io.Copy(io.Discard, member.reader)
			if err == nil {
				err = finishTarMember(s.records, member)
			}
			if err == nil {
				storage := tarStorage(s.file, member.start)
				if end := s.records.position(); member.position != nil && end != nil {
					storage.Metadata.GetTar().Header, storage.Metadata.GetTar().End = member.position, end
				}
				if err := emit(&InventoryEntry{Path: member.header.Name, Info: member.header.FileInfo(), Storage: storage}); err != nil {
					return err
				}
				continue
			}
		}

		// Only a readable incomplete tail ending at a clean filemark may be omitted.
		if s.records.err != nil {
			return s.records.err
		}
		if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return err
		}
		if !s.records.boundary.Filemark && !s.records.boundary.EOD {
			if err := s.consumeTrailer(); err != nil {
				return err
			}
		}
		if s.records.boundary.EOD {
			if s.records.offset != 0 {
				return errors.New("tar recorded tail has no terminating filemark")
			}
			return nil
		}
		if s.file == math.MaxUint64 {
			return errors.New("tar Tape file ordinal overflow")
		}
		s.file++
		s.records = s.recordReader(ctx, 0, s.records.nextObject, s.records.nextKnown)
	}
}

func (s *tarReadSession) consumeTrailer() error {
	// Legal fixed-record padding is zero; terminator completion still requires a physical filemark.
	var data [4096]byte
	for {
		n, err := s.records.Read(data[:])
		if !tarZeroBytes(data[:n]) {
			return errors.New("tar data follows archive terminator")
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
