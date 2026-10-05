package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"

	"github.com/asticode/go-astiav"
	"github.com/samuelncui/yatm/internal/previewprotocol"
)

func (m *media) render(width, height int, pad bool) (*image.NRGBA, error) {
	// Scale the decoded frame once into bounded, square-pixel RGB before rotation.
	rotation := m.rotation
	if angle, present := frameRotation(m.frame); present {
		if math.IsNaN(angle) || math.IsInf(angle, 0) {
			return nil, fmt.Errorf("invalid frame rotation")
		}
		rotation = int(math.Round(angle/90)) * 90
		if math.Abs(angle-float64(rotation)) > 0.1 {
			return nil, fmt.Errorf("unsupported frame rotation %.2f", angle)
		}
		rotation = (rotation%360 + 360) % 360
	}
	aspect := m.format.GuessSampleAspectRatio(m.stream, m.frame).Float64()
	if aspect <= 0 || math.IsNaN(aspect) || math.IsInf(aspect, 0) {
		aspect = 1
	}
	boundWidth, boundHeight := width, height
	if rotation == 90 || rotation == 270 {
		boundWidth, boundHeight = height, width
	}
	scale := math.Min(1, math.Min(float64(boundWidth)/(float64(m.frame.Width())*aspect), float64(boundHeight)/float64(m.frame.Height())))
	w := max(1, int(math.Round(float64(m.frame.Width())*aspect*scale)))
	h := max(1, int(math.Round(float64(m.frame.Height())*scale)))
	context, err := astiav.CreateSoftwareScaleContext(m.frame.Width(), m.frame.Height(), m.frame.PixelFormat(),
		w, h, astiav.PixelFormatRgba, astiav.NewSoftwareScaleContextFlags(astiav.SoftwareScaleContextFlagBilinear))
	if err != nil {
		return nil, fmt.Errorf("create scale context failed: %w", err)
	}
	defer context.Free()
	frame := astiav.AllocFrame()
	defer frame.Free()
	if err := context.ScaleFrame(m.frame, frame); err != nil {
		return nil, fmt.Errorf("scale frame failed: %w", err)
	}
	result := &image.NRGBA{}
	if err := frame.Data().ToImage(result); err != nil {
		return nil, fmt.Errorf("copy scaled frame failed: %w", err)
	}
	result = rotate(result, rotation)
	if !pad {
		return result, nil
	}

	// Fixed tile rectangles retain odd dimensions and letterbox without distorting aspect.
	canvas := image.NewNRGBA(image.Rect(0, 0, width, height))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: color.Black}, image.Point{}, draw.Src)
	offset := image.Pt((width-result.Bounds().Dx())/2, (height-result.Bounds().Dy())/2)
	draw.Draw(canvas, result.Bounds().Add(offset), result, image.Point{}, draw.Src)
	return canvas, nil
}

func rotate(source *image.NRGBA, angle int) *image.NRGBA {
	// Orthogonal rotation retains every pixel and requires no resampling buffer at source size.
	if angle == 0 {
		return source
	}
	w, h := source.Bounds().Dx(), source.Bounds().Dy()
	wOut, hOut := w, h
	if angle == 90 || angle == 270 {
		wOut, hOut = h, w
	}
	result := image.NewNRGBA(image.Rect(0, 0, wOut, hOut))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			xOut, yOut := w-1-x, h-1-y
			switch angle {
			case 90:
				xOut, yOut = h-1-y, x
			case 270:
				xOut, yOut = y, w-1-x
			}
			result.SetNRGBA(xOut, yOut, source.NRGBAAt(x, y))
		}
	}
	return result
}

type boundedWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *boundedWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		return 0, fmt.Errorf("asset exceeds byte limit")
	}
	n, err := w.writer.Write(data)
	w.remaining -= int64(n)
	return n, err
}

func saveImage(directory, role, format string, quality int, img *image.NRGBA) (previewprotocol.Asset, error) {
	// Exclusive creation prevents overwriting source or pre-existing caller data.
	name := role + "." + format
	file, err := os.OpenFile(filepath.Join(directory, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return previewprotocol.Asset{}, fmt.Errorf("create %s failed: %w", role, err)
	}
	writer := &boundedWriter{writer: file, remaining: maxAssetBytes}
	mediaType := "image/" + format
	switch format {
	case "png":
		err = png.Encode(writer, img)
	case "jpeg":
		err = jpeg.Encode(writer, img, &jpeg.Options{Quality: quality})
	case "webp":
		err = encodeWebP(writer, img, quality)
	default:
		err = fmt.Errorf("unsupported image format %q", format)
	}
	closeErr := file.Close()
	if err != nil {
		return previewprotocol.Asset{}, fmt.Errorf("encode %s failed: %w", role, err)
	}
	if closeErr != nil {
		return previewprotocol.Asset{}, fmt.Errorf("close %s failed: %w", role, closeErr)
	}
	return previewprotocol.Asset{Name: name, Role: role, MediaType: mediaType,
		Width: uint32(img.Bounds().Dx()), Height: uint32(img.Bounds().Dy())}, nil
}

func encodeWebP(writer io.Writer, img *image.NRGBA, quality int) error {
	// Let libwebp convert packed color itself, avoiding a second scaler and planar-alpha buffer.
	codec := astiav.FindEncoderByName("libwebp")
	if codec == nil {
		return fmt.Errorf("WebP encoder unavailable")
	}
	encoder := astiav.AllocCodecContext(codec)
	defer encoder.Free()
	encoder.SetWidth(img.Bounds().Dx())
	encoder.SetHeight(img.Bounds().Dy())
	encoder.SetPixelFormat(astiav.PixelFormatBgra)
	encoder.SetTimeBase(astiav.NewRational(1, 1))
	encoder.SetThreadCount(1)
	options := astiav.NewDictionary()
	defer options.Free()
	if err := options.Set("quality", strconv.Itoa(quality), 0); err != nil {
		return err
	}
	if err := encoder.Open(codec, options); err != nil {
		return fmt.Errorf("open WebP encoder failed: %w", err)
	}

	// Explicitly initialize every packed BGRA pixel; this preserves odd sizes and transparency.
	packed := image.NewNRGBA(image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy()))
	for y := 0; y < packed.Bounds().Dy(); y++ {
		for x := 0; x < packed.Bounds().Dx(); x++ {
			pixel := img.NRGBAAt(img.Bounds().Min.X+x, img.Bounds().Min.Y+y)
			index := y*packed.Stride + x*4
			packed.Pix[index], packed.Pix[index+1] = pixel.B, pixel.G
			packed.Pix[index+2], packed.Pix[index+3] = pixel.R, pixel.A
		}
	}
	input := astiav.AllocFrame()
	defer input.Free()
	input.SetWidth(img.Bounds().Dx())
	input.SetHeight(img.Bounds().Dy())
	input.SetPixelFormat(astiav.PixelFormatBgra)
	if err := input.AllocBuffer(32); err != nil {
		return err
	}
	if err := input.Data().FromImage(packed); err != nil {
		return err
	}
	input.SetPts(0)
	if err := encoder.SendFrame(input); err != nil {
		return err
	}
	packet := astiav.AllocPacket()
	defer packet.Free()
	if err := encoder.ReceivePacket(packet); err != nil {
		return err
	}
	_, err := writer.Write(packet.Data())
	return err
}
