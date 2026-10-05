package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"

	"github.com/asticode/go-astiav"
)

type media struct {
	format       *astiav.FormatContext
	codec        *astiav.CodecContext
	stream       *astiav.Stream
	packet       *astiav.Packet
	frame        *astiav.Frame
	interrupter  *astiav.IOInterrupter
	stop         chan struct{}
	done         chan struct{}
	duration     float64
	start        int64
	rotation     int
	maxPixels    int64
	startUnknown bool
	input        *os.File
	inputIO      *astiav.IOContext
	readBytes    int64
	readLimit    int64
}

func openMedia(ctx context.Context, path string, keyframes bool, pixels int64) (_ *media, err error) {
	// Install cancellation before opening the only input context used by this request.
	m := &media{format: astiav.AllocFormatContext(), maxPixels: pixels,
		interrupter: astiav.NewIOInterrupter(), stop: make(chan struct{}), done: make(chan struct{})}
	if m.format == nil {
		m.interrupter.Free()
		return nil, fmt.Errorf("allocate media context failed")
	}
	m.format.SetIOInterrupter(m.interrupter)
	go func() {
		defer close(m.done)
		select {
		case <-ctx.Done():
			m.interrupter.Interrupt()
		case <-m.stop:
		}
	}()
	defer func() {
		if err != nil {
			m.close()
		}
	}()

	// Custom seekable I/O bounds bytes consumed inside demuxer seeking as well as packet reads.
	m.input, err = os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open source failed: %w", err)
	}
	info, err := m.input.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened source failed: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("opened source is not a regular file")
	}
	m.readLimit = 64 << 20
	m.inputIO, err = astiav.AllocIOContext(64<<10, false, func(data []byte) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		remaining := m.readLimit - m.readBytes
		if remaining <= 0 {
			return 0, fmt.Errorf("sparse input byte budget exceeded")
		}
		if int64(len(data)) > remaining {
			data = data[:remaining]
		}
		n, err := m.input.Read(data)
		m.readBytes += int64(n)
		return n, err
	}, func(offset int64, whence int) (int64, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if whence&0x10000 != 0 {
			return info.Size(), nil
		} // AVSEEK_SIZE does not change position.
		whence &^= 0x20000 // AVSEEK_FORCE is a hint, not an os.File whence.
		if whence != io.SeekStart && whence != io.SeekCurrent && whence != io.SeekEnd {
			return 0, fmt.Errorf("invalid seek origin")
		}
		return m.input.Seek(offset, whence)
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("create bounded media input failed: %w", err)
	}
	m.format.SetPb(m.inputIO)
	options := astiav.NewDictionary()
	defer options.Free()
	if err = options.Set("protocol_whitelist", "file", 0); err != nil {
		return nil, err
	}
	if err = m.format.OpenInput(path, nil, options); err != nil {
		return nil, fmt.Errorf("open media failed: %w", err)
	}

	// Read only header facts: an implicit stream-info decode would bypass our decoder pixel limit.
	m.stream, _, err = m.format.FindBestStream(astiav.MediaTypeVideo, -1, -1)
	if err != nil {
		return nil, fmt.Errorf("container requires unsupported decoding probe: %w", err)
	}
	if m.stream.CodecParameters().CodecID() == astiav.CodecIDNone {
		return nil, fmt.Errorf("visual codec unavailable in container headers")
	}
	parameters := m.stream.CodecParameters()
	if keyframes && m.stream.TimeBase().Float64() <= 0 {
		return nil, fmt.Errorf("video time base unavailable")
	}
	if err = checkPixels(parameters.Width(), parameters.Height(), pixels, true); err != nil {
		return nil, err
	}
	decoder := astiav.FindDecoder(parameters.CodecID())
	if decoder == nil {
		return nil, fmt.Errorf("decoder unavailable for %s", parameters.CodecID())
	}
	m.codec = astiav.AllocCodecContext(decoder)
	if m.codec == nil {
		return nil, fmt.Errorf("allocate decoder failed")
	}
	if err = m.codec.FromCodecParameters(parameters); err != nil {
		return nil, fmt.Errorf("configure decoder failed: %w", err)
	}
	m.codec.SetThreadCount(2)
	m.codec.SetThreadType(astiav.ThreadTypeSlice)
	configureDecoder(m.codec, pixels, keyframes)
	if err = m.codec.Open(decoder, nil); err != nil {
		return nil, fmt.Errorf("open decoder failed: %w", err)
	}

	// Normalize stream timestamps, accounting for containers with non-zero starts.
	m.start = m.stream.StartTime()
	if m.start == astiav.NoPtsValue {
		m.startUnknown = true
		m.start = 0
	}
	m.duration = float64(m.stream.Duration()) * m.stream.TimeBase().Float64()
	if m.stream.Duration() == astiav.NoPtsValue || m.duration <= 0 {
		m.duration = float64(m.format.Duration()) / float64(astiav.TimeBase)
	}
	if keyframes && (m.duration <= 0 || math.IsInf(m.duration, 0) || math.IsNaN(m.duration)) {
		return nil, fmt.Errorf("video duration unavailable")
	}
	if matrix, ok := parameters.SideData().DisplayMatrix().Get(); ok {
		angle := matrix.Rotation()
		if math.IsNaN(angle) {
			return nil, fmt.Errorf("invalid display rotation")
		}
		m.rotation = int(math.Round(angle/90)) * 90
		if math.Abs(angle-float64(m.rotation)) > 0.1 {
			return nil, fmt.Errorf("unsupported non-orthogonal rotation %.2f", angle)
		}
	} else if metadata := m.stream.Metadata(); metadata != nil {
		if entry := metadata.Get("rotate", nil, 0); entry != nil {
			angle, parseErr := strconv.Atoi(entry.Value())
			if parseErr != nil || angle%90 != 0 {
				return nil, fmt.Errorf("unsupported display rotation %q", entry.Value())
			}
			m.rotation = angle
		}
	}
	m.rotation = (m.rotation%360 + 360) % 360
	m.packet, m.frame = astiav.AllocPacket(), astiav.AllocFrame()
	if m.packet == nil || m.frame == nil {
		return nil, fmt.Errorf("allocate decoding buffers failed")
	}
	return m, nil
}

func (m *media) close() {
	// Stop the interrupter user before releasing native resources it may reference.
	close(m.stop)
	<-m.done
	if m.frame != nil {
		m.frame.Free()
	}
	if m.packet != nil {
		m.packet.Free()
	}
	if m.codec != nil {
		m.codec.Free()
	}
	m.format.CloseInput()
	m.format.Free()
	if m.inputIO != nil {
		m.inputIO.Free()
	}
	if m.input != nil {
		_ = m.input.Close()
	}
	m.interrupter.Free()
}

func (m *media) sample(ctx context.Context, seconds float64, seek bool) (float64, error) {
	// Each sample starts from a preceding container seek point, never from a full-film decode.
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	m.frame.Unref()
	m.readLimit = m.readBytes + 64<<20
	if seek {
		timestamp := m.start + int64(seconds/m.stream.TimeBase().Float64())
		if err := m.format.SeekFrame(m.stream.Index(), timestamp, astiav.NewSeekFlags(astiav.SeekFlagBackward)); err != nil {
			return 0, fmt.Errorf("seek %.3fs failed: %w", seconds, err)
		}
		flushDecoder(m.codec)
	}

	// Bounds cover malformed indexes and streams that never produce a usable keyframe.
	bytesRead, packets := 0, 0
	draining := false
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		err := m.codec.ReceiveFrame(m.frame)
		if err == nil {
			if err := checkPixels(m.frame.Width(), m.frame.Height(), m.maxPixels, false); err != nil {
				return 0, err
			}
			if m.frame.Flags().Has(astiav.FrameFlagCorrupt) {
				return 0, fmt.Errorf("decoder returned a corrupt frame")
			}
			if flags := frameDecodeErrors(m.frame); flags != 0 {
				return 0, fmt.Errorf("decoder returned concealed or incomplete content (flags=%d)", flags)
			}
			if seek && !m.frame.Flags().Has(astiav.FrameFlagKey) {
				return 0, fmt.Errorf("decoder did not return a keyframe")
			}
			pts := frameTimestamp(m.frame)
			if pts == astiav.NoPtsValue {
				pts = m.frame.Pts()
			}
			if pts == astiav.NoPtsValue && seek {
				return 0, fmt.Errorf("sample has no timestamp")
			}
			if pts == astiav.NoPtsValue {
				return 0, nil
			}
			// Header-only Matroska discovery can omit start_time; establish it from the first keyframe.
			if seek && m.startUnknown {
				m.start = pts
				m.startUnknown = false
				if m.stream.Duration() == astiav.NoPtsValue || m.stream.Duration() <= 0 {
					m.duration -= math.Max(0, float64(pts)*m.stream.TimeBase().Float64())
				}
				if m.duration <= 0 {
					return 0, fmt.Errorf("video has no duration after its first frame")
				}
			}
			return math.Max(0, float64(pts-m.start)*m.stream.TimeBase().Float64()), nil
		}
		if !errors.Is(err, astiav.ErrEagain) {
			return 0, fmt.Errorf("decode sample %.3fs failed: %w", seconds, err)
		}
		if draining {
			return 0, fmt.Errorf("no frame after decoder drain")
		}
		if packets >= 4096 || bytesRead >= 64<<20 {
			return 0, fmt.Errorf("sample %.3fs exceeded sparse-read budget", seconds)
		}
		m.packet.Unref()
		if err := m.format.ReadFrame(m.packet); err != nil {
			if !errors.Is(err, astiav.ErrEof) {
				return 0, fmt.Errorf("read sample failed: %w", err)
			}
			if err := m.codec.SendPacket(nil); err != nil {
				return 0, fmt.Errorf("drain sample failed: %w", err)
			}
			draining = true
			continue
		}
		packets++
		bytesRead += m.packet.Size()
		if m.packet.StreamIndex() != m.stream.Index() {
			continue
		}
		if err := m.codec.SendPacket(m.packet); err != nil {
			return 0, fmt.Errorf("submit sample packet failed: %w", err)
		}
	}
}

func checkPixels(width, height int, limit int64, allowUnknown bool) error {
	if allowUnknown && (width == 0 || height == 0) {
		return nil
	}
	if width <= 0 || height <= 0 || int64(width)*int64(height) > limit {
		return fmt.Errorf("input dimensions %dx%d exceed pixel limit %d", width, height, limit)
	}
	return nil
}
