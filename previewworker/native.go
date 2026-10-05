package main

/*
#cgo pkg-config: libavcodec libavutil
#include <libavcodec/avcodec.h>
#include <libavutil/avutil.h>
#include <libavutil/display.h>
#include <libavutil/mem.h>
static void configure_decoder(AVCodecContext *ctx, int64_t pixels, int keyframes) {
    ctx->max_pixels = pixels;
    if (keyframes) ctx->skip_frame = AVDISCARD_NONKEY;
}
static int64_t frame_timestamp(AVFrame *frame) { return frame->best_effort_timestamp; }
static int frame_decode_errors(AVFrame *frame) { return frame->decode_error_flags; }
static int frame_rotation(AVFrame *frame, double *angle) {
    AVFrameSideData *data = av_frame_get_side_data(frame, AV_FRAME_DATA_DISPLAYMATRIX);
    if (!data || data->size < 9 * sizeof(int32_t)) return 0;
    *angle = -av_display_rotation_get((const int32_t *)data->data);
    return 1;
}
*/
import "C"

import "github.com/asticode/go-astiav"

func configureDecoder(codec *astiav.CodecContext, pixels int64, keyframes bool) {
	key := 0
	if keyframes {
		key = 1
	}
	C.configure_decoder((*C.AVCodecContext)(codec.UnsafePointer()), C.int64_t(pixels), C.int(key))
}

func flushDecoder(codec *astiav.CodecContext) {
	C.avcodec_flush_buffers((*C.AVCodecContext)(codec.UnsafePointer()))
}

func frameTimestamp(frame *astiav.Frame) int64 {
	return int64(C.frame_timestamp((*C.AVFrame)(frame.UnsafePointer())))
}

func frameDecodeErrors(frame *astiav.Frame) int {
	return int(C.frame_decode_errors((*C.AVFrame)(frame.UnsafePointer())))
}

func frameRotation(frame *astiav.Frame) (float64, bool) {
	var angle C.double
	present := C.frame_rotation((*C.AVFrame)(frame.UnsafePointer()), &angle)
	return float64(angle), present != 0
}

func nativeVersion() string { return C.GoString(C.av_version_info()) }

func limitNativeAllocation() { C.av_max_alloc(256 << 20) }
