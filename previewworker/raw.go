package main

/*
#cgo pkg-config: libraw
#include <libraw/libraw.h>
#include <stdlib.h>
#include <stdatomic.h>
typedef struct { atomic_int cancelled; } raw_cancel;
static raw_cancel *raw_cancel_new(void) {
    raw_cancel *c = malloc(sizeof(raw_cancel));
    if (c) atomic_init(&c->cancelled, 0);
    return c;
}
static void raw_cancel_set(raw_cancel *c) { atomic_store(&c->cancelled, 1); }
static int raw_progress(void *p, enum LibRaw_progress stage, int iteration, int expected) {
    return atomic_load(&((raw_cancel *)p)->cancelled);
}
static void raw_setup(libraw_data_t *r, raw_cancel *c, unsigned memory_mb) {
    libraw_set_progress_handler(r, raw_progress, c);
    r->rawparams.max_raw_memory_mb = memory_mb;
    r->params.output_color = 1;
    r->params.output_bps = 8;
    r->params.use_camera_wb = 1;
    // Since LibRaw 0.20, camera WB automatically falls back to auto when metadata has no camera multiplier.
    r->params.use_auto_wb = 0;
    r->params.user_qual = 0;
    r->params.user_flip = 0;
}
static unsigned char *raw_pixels(libraw_processed_image_t *p) { return p->data; }
*/
import "C"

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"unsafe"

	"github.com/samuelncui/yatm/internal/previewprotocol"
	"golang.org/x/image/draw"
)

var rawExtensions = []string{"dng", "cr2", "cr3", "nef", "nrw", "arw", "raf", "orf", "rw2", "pef", "srw"}

func isRAW(path string) bool {
	return slices.Contains(rawExtensions, strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), "."))
}

func rawVersion() string { return C.GoString(C.libraw_version()) }

func generateRAW(ctx context.Context, request previewprotocol.Request, report progress) ([]previewprotocol.Asset, error) {
	// Keep all native storage scoped to one file and make cancellation safe across C calls.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r := C.libraw_init(0)
	if r == nil {
		return nil, fmt.Errorf("allocate RAW decoder failed")
	}
	cancel := C.raw_cancel_new()
	if cancel == nil {
		C.libraw_close(r)
		return nil, fmt.Errorf("allocate RAW cancellation state failed")
	}
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			C.raw_cancel_set(cancel)
		case <-done:
		}
	}()
	defer func() {
		close(done)
		<-stopped
		C.libraw_close(r)
		C.free(unsafe.Pointer(cancel))
	}()
	C.raw_setup(r, cancel, C.uint(max(32, min(512, request.MaxInputPixels*8/(1<<20)))))
	path := C.CString(request.SourcePath)
	defer C.free(unsafe.Pointer(path))
	if err := rawError(ctx, "open", C.libraw_open_file(r, path)); err != nil {
		return nil, err
	}
	width, height := int(r.sizes.width), int(r.sizes.height)
	if width <= 0 || height <= 0 || int64(r.sizes.raw_width)*int64(r.sizes.raw_height) > request.MaxInputPixels {
		return nil, fmt.Errorf("RAW dimensions exceed input pixel limit")
	}
	flip := int(r.sizes.flip)
	if flip < 0 || flip > 7 {
		return nil, fmt.Errorf("unsupported RAW orientation %d", flip)
	}
	if err := report("opened", 0, 0); err != nil {
		return nil, err
	}

	// Prefer the smallest embedded preview that can satisfy the output without upscaling.
	wantWidth, wantHeight := rawOutputSize(width, height, flip, request.MaxWidth, request.MaxHeight)
	if r.thumbs_list.thumbcount < 0 || int(r.thumbs_list.thumbcount) > len(r.thumbs_list.thumblist) {
		return nil, fmt.Errorf("invalid RAW thumbnail count")
	}
	indices := make([]int, int(r.thumbs_list.thumbcount))
	for i := range indices {
		indices[i] = i
	}
	slices.SortFunc(indices, func(a, b int) int {
		x, y := r.thumbs_list.thumblist[a], r.thumbs_list.thumblist[b]
		return int(x.twidth)*int(x.theight) - int(y.twidth)*int(y.theight)
	})
	var img *image.NRGBA
	for _, index := range indices {
		thumb := r.thumbs_list.thumblist[index]
		thumbFlip := int(thumb.tflip)
		if thumbFlip == 0xffff {
			thumbFlip = flip
		}
		w, h := int(thumb.twidth), int(thumb.theight)
		if thumbFlip&4 != 0 {
			w, h = h, w
		}
		if (w > 0 && h > 0 && (w < wantWidth || h < wantHeight)) || int64(w)*int64(h) > request.MaxInputPixels || thumb.tlength > 64<<20 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if C.libraw_unpack_thumb_ex(r, C.int(index)) != 0 {
			continue
		}
		var code C.int
		pixels := C.libraw_dcraw_make_mem_thumb(r, &code)
		if pixels == nil {
			continue
		}
		img, _ = renderRAWMemory(ctx, pixels, thumbFlip, wantWidth, wantHeight, request.MaxInputPixels, true)
		C.libraw_dcraw_clear_mem(pixels)
		if img != nil {
			break
		}
	}

	// Actual development is the bounded fallback; half-size is enough for small derivatives.
	if img == nil {
		if err := report("developing", 0, 1); err != nil {
			return nil, err
		}
		boundWidth, boundHeight := request.MaxWidth, request.MaxHeight
		if flip&4 != 0 {
			boundWidth, boundHeight = boundHeight, boundWidth
		}
		if width/2 >= boundWidth && height/2 >= boundHeight {
			r.params.half_size = 1
		}
		if err := rawError(ctx, "unpack", C.libraw_unpack(r)); err != nil {
			return nil, err
		}
		if err := rawError(ctx, "develop", C.libraw_dcraw_process(r)); err != nil {
			return nil, err
		}
		var code C.int
		pixels := C.libraw_dcraw_make_mem_image(r, &code)
		if pixels == nil {
			if err := rawError(ctx, "render", code); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("RAW render returned no image")
		}
		img, err := renderRAWMemory(ctx, pixels, flip, wantWidth, wantHeight, request.MaxInputPixels, false)
		C.libraw_dcraw_clear_mem(pixels)
		if err != nil {
			return nil, err
		}
		return saveRAW(ctx, request, report, img)
	}
	return saveRAW(ctx, request, report, img)
}

func saveRAW(ctx context.Context, request previewprotocol.Request, report progress, img *image.NRGBA) ([]previewprotocol.Asset, error) {
	// Publish the same role and encoding as all other Image sources.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := report("encoding", 0, 1); err != nil {
		return nil, err
	}
	asset, err := saveImage(request.OutputDir, "thumbnail", request.Format, request.Quality, img)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []previewprotocol.Asset{asset}, nil
}

func rawError(ctx context.Context, operation string, code C.int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if code == 0 {
		return nil
	}
	return fmt.Errorf("RAW %s failed: %s", operation, C.GoString(C.libraw_strerror(code)))
}

func rawOutputSize(width, height, flip, maxWidth, maxHeight int) (int, int) {
	if flip&4 != 0 {
		width, height = height, width
	}
	scale := math.Min(1, math.Min(float64(maxWidth)/float64(width), float64(maxHeight)/float64(height)))
	return max(1, int(math.Round(float64(width)*scale))), max(1, int(math.Round(float64(height)*scale)))
}

// rawBitmap views native RGB bytes without copying a full-resolution image into Go memory.
type rawBitmap struct {
	data          []byte
	width, height int
}

func (b rawBitmap) ColorModel() color.Model { return color.RGBA64Model }
func (b rawBitmap) Bounds() image.Rectangle { return image.Rect(0, 0, b.width, b.height) }
func (b rawBitmap) At(x, y int) color.Color {
	return b.RGBA64At(x, y)
}
func (b rawBitmap) RGBA64At(x, y int) color.RGBA64 {
	if x < 0 || y < 0 || x >= b.width || y >= b.height {
		return color.RGBA64{}
	}
	i := (y*b.width + x) * 3
	return color.RGBA64{uint16(b.data[i]) * 257, uint16(b.data[i+1]) * 257, uint16(b.data[i+2]) * 257, 65535}
}

func renderRAWMemory(ctx context.Context, pixels *C.libraw_processed_image_t, flip, width, height int, limit int64, requireSize bool) (*image.NRGBA, error) {
	// Validate compressed dimensions before decoding JPEG and validate bitmap storage before viewing it.
	if flip < 0 || flip > 7 || pixels.data_size == 0 || pixels.data_size > 256<<20 {
		return nil, fmt.Errorf("invalid RAW preview buffer")
	}
	data := unsafe.Slice((*byte)(unsafe.Pointer(C.raw_pixels(pixels))), int(pixels.data_size))
	var source image.Image
	if pixels._type == C.LIBRAW_IMAGE_JPEG {
		config, err := jpeg.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		if int64(config.Width)*int64(config.Height) > limit {
			return nil, fmt.Errorf("RAW embedded JPEG exceeds pixel limit")
		}
		source, err = jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
	} else if pixels._type == C.LIBRAW_IMAGE_BITMAP && pixels.bits == 8 && pixels.colors == 3 {
		w, h := int(pixels.width), int(pixels.height)
		if w <= 0 || h <= 0 || int64(w)*int64(h) > limit || int64(w)*int64(h)*3 != int64(len(data)) {
			return nil, fmt.Errorf("invalid RAW bitmap dimensions")
		}
		source = rawBitmap{data, w, h}
	} else {
		return nil, fmt.Errorf("unsupported RAW preview encoding")
	}

	// Resize before orientation so only a target-sized RGBA buffer needs transformation.
	boundWidth, boundHeight := width, height
	if flip&4 != 0 {
		boundWidth, boundHeight = height, width
	}
	if requireSize && (source.Bounds().Dx() < boundWidth || source.Bounds().Dy() < boundHeight) {
		return nil, fmt.Errorf("embedded preview is too small")
	}
	w, h := rawOutputSize(source.Bounds().Dx(), source.Bounds().Dy(), 0, boundWidth, boundHeight)
	result := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(result, result.Bounds(), source, source.Bounds(), draw.Src, nil)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return orientRAW(result, flip), nil
}

func orientRAW(source *image.NRGBA, flip int) *image.NRGBA {
	// LibRaw uses transpose, vertical mirror and horizontal mirror bits, not EXIF orientation numbers.
	w, h := source.Bounds().Dx(), source.Bounds().Dy()
	wOut, hOut := w, h
	if flip&4 != 0 {
		wOut, hOut = h, w
	}
	result := image.NewNRGBA(image.Rect(0, 0, wOut, hOut))
	for y := 0; y < hOut; y++ {
		for x := 0; x < wOut; x++ {
			sx, sy := x, y
			if flip&4 != 0 {
				sx, sy = sy, sx
			}
			if flip&2 != 0 {
				sy = h - 1 - sy
			}
			if flip&1 != 0 {
				sx = w - 1 - sx
			}
			result.SetNRGBA(x, y, source.NRGBAAt(sx, sy))
		}
	}
	return result
}
