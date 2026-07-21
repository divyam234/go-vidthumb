#pragma once
#include <stdint.h>

typedef struct PVDecoder PVDecoder;

typedef struct PVInfo {
    double duration;
    int width;
    int height;
    double fps;
    char video_codec[64];
    char audio_codec[64];
    int64_t bitrate;
} PVInfo;

typedef struct PVThumb {
    int width;
    int height;
    int stride;
    uint8_t *rgba;
    int rgba_size;
} PVThumb;

typedef struct PVPreviewSliceMeta {
    double requested_start;
    double actual_start;
    double inpoint;
    double outpoint;
    double copied_duration;
} PVPreviewSliceMeta;

void pv_set_log_level(int level);
int pv_get_log_level(void);
int pv_probe(const char *input_path, PVInfo *info);
const char *pv_last_error(void);
PVDecoder *pv_decoder_open(const char *input_path);
void pv_decoder_close(PVDecoder *d);
int pv_seek_thumbnail(PVDecoder *d, double seconds, int target_width, PVThumb *out);
int pv_seek_thumbnail_bicubic(PVDecoder *d, double seconds, int target_width, PVThumb *out);
void pv_thumb_free(PVThumb *t);
int pv_copy_video_slice(const char *input_path, const char *output_path, double start_seconds, double slice_seconds);
int pv_copy_video_slice_meta(const char *input_path, const char *output_path, double start_seconds, double slice_seconds, PVPreviewSliceMeta *meta);
int pv_concat_video_segments(const char *list_path, const char *output_path);
int pv_transcode_video_resize(const char *input_path, const char *output_path, int target_width);
int pv_transcode_concat_video(const char *list_path, const char *output_path, int target_width, double target_fps);
