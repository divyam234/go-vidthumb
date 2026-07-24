package previewer

/*
#cgo pkg-config: libavformat libavcodec libavutil libswscale libswresample libavfilter
#cgo LDFLAGS: -lm
#include <stdlib.h>
#include "ffmpeg_bridge.h"
*/
import "C"

import (
	"fmt"
	"unsafe"
)

type MediaInfo struct {
	Duration   float64
	Width      int
	Height     int
	FPS        float64
	VideoCodec string
	AudioCodec string
	Bitrate    int64
}

// LogLevel controls process-wide FFmpeg library logging.
type LogLevel int

const (
	LogLevelQuiet   LogLevel = -8
	LogLevelPanic   LogLevel = 0
	LogLevelFatal   LogLevel = 8
	LogLevelError   LogLevel = 16
	LogLevelWarning LogLevel = 24
	LogLevelInfo    LogLevel = 32
	LogLevelVerbose LogLevel = 40
	LogLevelDebug   LogLevel = 48
	LogLevelTrace   LogLevel = 56
)

// SetLogLevel sets the process-wide FFmpeg log level. The default is LogLevelError.
func SetLogLevel(level LogLevel) {
	C.pv_set_log_level(C.int(level))
}

// CurrentLogLevel returns the process-wide FFmpeg log level.
func CurrentLogLevel() LogLevel {
	return LogLevel(C.pv_get_log_level())
}

type cDecoder struct{ ptr *C.PVDecoder }

func lastFFErr() string {
	s := C.pv_last_error()
	if s == nil {
		return "unknown ffmpeg error"
	}
	return C.GoString(s)
}

func Probe(input string) (MediaInfo, error) {
	cin := C.CString(input)
	defer C.free(unsafe.Pointer(cin))
	var info C.PVInfo
	if rc := C.pv_probe(cin, &info); rc < 0 {
		return MediaInfo{}, fmt.Errorf("probe failed: %s", lastFFErr())
	}
	return MediaInfo{
		Duration:   float64(info.duration),
		Width:      int(info.width),
		Height:     int(info.height),
		FPS:        float64(info.fps),
		VideoCodec: C.GoString(&info.video_codec[0]),
		AudioCodec: C.GoString(&info.audio_codec[0]),
		Bitrate:    int64(info.bitrate),
	}, nil
}

func openDecoder(input string) (*cDecoder, error) {
	cin := C.CString(input)
	defer C.free(unsafe.Pointer(cin))
	ptr := C.pv_decoder_open(cin)
	if ptr == nil {
		return nil, fmt.Errorf("open decoder failed: %s", lastFFErr())
	}
	return &cDecoder{ptr: ptr}, nil
}

func (d *cDecoder) Close() {
	if d != nil && d.ptr != nil {
		C.pv_decoder_close(d.ptr)
		d.ptr = nil
	}
}

type Thumb struct {
	Index  int
	Start  float64
	End    float64
	Width  int
	Height int
	RGBA   []byte
}

func (d *cDecoder) SeekThumbnail(seconds float64, targetWidth int) (Thumb, error) {
	return d.seekThumbnail(seconds, targetWidth, false, false)
}

func (d *cDecoder) SeekThumbnailBicubic(seconds float64, targetWidth int) (Thumb, error) {
	return d.seekThumbnail(seconds, targetWidth, true, false)
}

func (d *cDecoder) SeekThumbnailFast(seconds float64, targetWidth int) (Thumb, error) {
	return d.seekThumbnail(seconds, targetWidth, false, true)
}

func (d *cDecoder) SeekThumbnailFastBicubic(seconds float64, targetWidth int) (Thumb, error) {
	return d.seekThumbnail(seconds, targetWidth, true, true)
}

func (d *cDecoder) seekThumbnail(seconds float64, targetWidth int, bicubic, fast bool) (Thumb, error) {
	var out C.PVThumb
	var rc C.int
	if fast {
		if bicubic {
			rc = C.pv_seek_thumbnail_fast_bicubic(d.ptr, C.double(seconds), C.int(targetWidth), &out)
		} else {
			rc = C.pv_seek_thumbnail_fast(d.ptr, C.double(seconds), C.int(targetWidth), &out)
		}
	} else if bicubic {
		rc = C.pv_seek_thumbnail_bicubic(d.ptr, C.double(seconds), C.int(targetWidth), &out)
	} else {
		rc = C.pv_seek_thumbnail(d.ptr, C.double(seconds), C.int(targetWidth), &out)
	}
	if rc < 0 {
		return Thumb{}, fmt.Errorf("seek thumbnail %.3fs failed: %s", seconds, lastFFErr())
	}
	defer C.pv_thumb_free(&out)

	if out.rgba == nil || out.width <= 0 || out.height <= 0 || out.stride <= 0 {
		return Thumb{}, fmt.Errorf("seek thumbnail %.3fs returned empty image", seconds)
	}

	width := int(out.width)
	height := int(out.height)
	stride := int(out.stride)
	raw := C.GoBytes(unsafe.Pointer(out.rgba), C.int(out.rgba_size))

	// av_image_alloc may include padding. Compact to Go image.RGBA stride width*4.
	compact := make([]byte, width*height*4)
	for y := 0; y < height; y++ {
		copy(compact[y*width*4:(y+1)*width*4], raw[y*stride:y*stride+width*4])
	}

	return Thumb{Width: width, Height: height, RGBA: compact}, nil
}

type copiedSliceMeta struct {
	RequestedStart float64
	ActualStart    float64
	Inpoint        float64
	Outpoint       float64
	CopiedDuration float64
}

func CopyVideoSlice(input, output string, startSeconds, sliceSeconds float64) error {
	_, err := CopyVideoSliceDetailed(input, output, startSeconds, sliceSeconds)
	return err
}

func CopyVideoSliceDetailed(input, output string, startSeconds, sliceSeconds float64) (copiedSliceMeta, error) {
	cin := C.CString(input)
	cout := C.CString(output)
	defer C.free(unsafe.Pointer(cin))
	defer C.free(unsafe.Pointer(cout))
	var meta C.PVPreviewSliceMeta
	if rc := C.pv_copy_video_slice_meta(cin, cout, C.double(startSeconds), C.double(sliceSeconds), &meta); rc < 0 {
		return copiedSliceMeta{}, fmt.Errorf("copy preview slice %.3fs+%.3fs failed: %s", startSeconds, sliceSeconds, lastFFErr())
	}
	return copiedSliceMeta{
		RequestedStart: float64(meta.requested_start),
		ActualStart:    float64(meta.actual_start),
		Inpoint:        float64(meta.inpoint),
		Outpoint:       float64(meta.outpoint),
		CopiedDuration: float64(meta.copied_duration),
	}, nil
}

func TranscodeVideoSlice(input, output string, startSeconds, durationSeconds float64, targetWidth int, targetFPS float64) error {
	cin := C.CString(input)
	cout := C.CString(output)
	defer C.free(unsafe.Pointer(cin))
	defer C.free(unsafe.Pointer(cout))
	if rc := C.pv_transcode_video_slice(cin, cout, C.double(startSeconds), C.double(durationSeconds), C.int(targetWidth), C.double(targetFPS)); rc < 0 {
		return fmt.Errorf("transcode preview slice %.3fs+%.3fs failed: %s", startSeconds, durationSeconds, lastFFErr())
	}
	return nil
}

func ConcatVideoSegments(listPath, output string) error {
	clist := C.CString(listPath)
	cout := C.CString(output)
	defer C.free(unsafe.Pointer(clist))
	defer C.free(unsafe.Pointer(cout))
	if rc := C.pv_concat_video_segments(clist, cout); rc < 0 {
		return fmt.Errorf("concat preview segments failed: %s", lastFFErr())
	}
	return nil
}

func remuxFile(input, output string) error {
	cin := C.CString(input)
	cout := C.CString(output)
	defer C.free(unsafe.Pointer(cin))
	defer C.free(unsafe.Pointer(cout))
	if rc := C.pv_remux_file(cin, cout); rc < 0 {
		return fmt.Errorf("remux file failed: %s", lastFFErr())
	}
	return nil
}

func TranscodeVideoResize(input, output string, targetWidth int) error {
	cin := C.CString(input)
	cout := C.CString(output)
	defer C.free(unsafe.Pointer(cin))
	defer C.free(unsafe.Pointer(cout))
	if rc := C.pv_transcode_video_resize(cin, cout, C.int(targetWidth)); rc < 0 {
		return fmt.Errorf("final preview resize/transcode failed: %s", lastFFErr())
	}
	return nil
}

func TranscodeConcatVideo(listPath, output string, targetWidth int, targetFPS float64) error {
	clist := C.CString(listPath)
	cout := C.CString(output)
	defer C.free(unsafe.Pointer(clist))
	defer C.free(unsafe.Pointer(cout))
	if rc := C.pv_transcode_concat_video(clist, cout, C.int(targetWidth), C.double(targetFPS)); rc < 0 {
		return fmt.Errorf("final concat/transcode failed: %s", lastFFErr())
	}
	return nil
}
