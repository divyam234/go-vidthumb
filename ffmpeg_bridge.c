#include "ffmpeg_bridge.h"

#include <libavformat/avformat.h>
#include <libavcodec/avcodec.h>
#include <libavutil/avutil.h>
#include <libavutil/imgutils.h>
#include <libavutil/opt.h>
#include <libswscale/swscale.h>
#include <libavfilter/avfilter.h>
#include <libavfilter/buffersrc.h>
#include <libavfilter/buffersink.h>

#include <errno.h>
#include <math.h>
#include <stdarg.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define MAX_DECODE_PACKETS 4096

static __thread char g_last_error[1024];
static pthread_once_t g_ffmpeg_init_once = PTHREAD_ONCE_INIT;

static void pv_ffmpeg_init_once(void) {
    av_log_set_level(AV_LOG_ERROR);
    avformat_network_init();
}

static void pv_ffmpeg_init(void) {
    pthread_once(&g_ffmpeg_init_once, pv_ffmpeg_init_once);
}

static void set_error(const char *fmt, ...) {
    va_list ap;
    va_start(ap, fmt);
    vsnprintf(g_last_error, sizeof(g_last_error), fmt, ap);
    va_end(ap);
}

static void set_av_error(const char *prefix, int err) {
    char buf[AV_ERROR_MAX_STRING_SIZE] = {0};
    av_strerror(err, buf, sizeof(buf));
    set_error("%s: %s (%d)", prefix, buf, err);
}

const char *pv_last_error(void) {
    return g_last_error;
}

void pv_decoder_close(PVDecoder *d);

struct PVDecoder {
    AVFormatContext *fmt;
    AVCodecContext *dec;
    AVStream *stream;
    int stream_index;
    AVPacket *pkt;
    AVFrame *frame;
    struct SwsContext *sws;
    int sws_src_w;
    int sws_src_h;
    enum AVPixelFormat sws_src_fmt;
    int sws_dst_w;
    int sws_dst_h;
};

static double stream_duration_seconds(AVFormatContext *fmt, AVStream *st) {
    if (fmt && fmt->duration > 0) {
        return (double)fmt->duration / (double)AV_TIME_BASE;
    }
    if (st && st->duration > 0) {
        return (double)st->duration * av_q2d(st->time_base);
    }
    return 0.0;
}

static double stream_fps(AVStream *st) {
    if (!st) return 0.0;
    if (st->avg_frame_rate.num > 0 && st->avg_frame_rate.den > 0) {
        double fps = av_q2d(st->avg_frame_rate);
        if (fps > 0.0 && fps < 240.0) return fps;
    }
    if (st->r_frame_rate.num > 0 && st->r_frame_rate.den > 0) {
        double fps = av_q2d(st->r_frame_rate);
        if (fps > 0.0 && fps < 240.0) return fps;
    }
    return 30.0;
}

int pv_probe(const char *input_path, PVInfo *info) {
    if (!input_path || !info) {
        set_error("pv_probe: nil input");
        return AVERROR(EINVAL);
    }
    memset(info, 0, sizeof(*info));
    pv_ffmpeg_init();

    AVFormatContext *fmt = NULL;
    int ret = avformat_open_input(&fmt, input_path, NULL, NULL);
    if (ret < 0) { set_av_error("avformat_open_input", ret); return ret; }

    ret = avformat_find_stream_info(fmt, NULL);
    if (ret < 0) { set_av_error("avformat_find_stream_info", ret); avformat_close_input(&fmt); return ret; }

    int idx = av_find_best_stream(fmt, AVMEDIA_TYPE_VIDEO, -1, -1, NULL, 0);
    if (idx < 0) { set_av_error("av_find_best_stream(video)", idx); avformat_close_input(&fmt); return idx; }

    AVStream *st = fmt->streams[idx];
    info->duration = stream_duration_seconds(fmt, st);
    info->width = st->codecpar->width;
    info->height = st->codecpar->height;
    info->fps = stream_fps(st);

    avformat_close_input(&fmt);
    return 0;
}

static PVDecoder *pv_decoder_open_with_input_format(const char *input_path, const AVInputFormat *iformat, AVDictionary **opts) {
    if (!input_path) {
        set_error("pv_decoder_open: nil input");
        return NULL;
    }
    pv_ffmpeg_init();

    PVDecoder *d = (PVDecoder *)calloc(1, sizeof(PVDecoder));
    if (!d) { set_error("calloc decoder failed"); return NULL; }

    int ret = avformat_open_input(&d->fmt, input_path, iformat, opts);
    if (ret < 0) { set_av_error("avformat_open_input", ret); pv_decoder_close(d); return NULL; }

    ret = avformat_find_stream_info(d->fmt, NULL);
    if (ret < 0) { set_av_error("avformat_find_stream_info", ret); pv_decoder_close(d); return NULL; }

    d->stream_index = av_find_best_stream(d->fmt, AVMEDIA_TYPE_VIDEO, -1, -1, NULL, 0);
    if (d->stream_index < 0) { set_av_error("av_find_best_stream(video)", d->stream_index); pv_decoder_close(d); return NULL; }
    d->stream = d->fmt->streams[d->stream_index];

    const AVCodec *codec = avcodec_find_decoder(d->stream->codecpar->codec_id);
    if (!codec) { set_error("decoder not found for codec id %d", d->stream->codecpar->codec_id); pv_decoder_close(d); return NULL; }

    d->dec = avcodec_alloc_context3(codec);
    if (!d->dec) { set_error("avcodec_alloc_context3 failed"); pv_decoder_close(d); return NULL; }

    ret = avcodec_parameters_to_context(d->dec, d->stream->codecpar);
    if (ret < 0) { set_av_error("avcodec_parameters_to_context", ret); pv_decoder_close(d); return NULL; }
    ret = avcodec_open2(d->dec, codec, NULL);
    if (ret < 0) { set_av_error("avcodec_open2 decoder", ret); pv_decoder_close(d); return NULL; }

    d->pkt = av_packet_alloc();
    d->frame = av_frame_alloc();
    if (!d->pkt || !d->frame) { set_error("packet/frame alloc failed"); pv_decoder_close(d); return NULL; }

    d->sws_src_w = d->sws_src_h = d->sws_dst_w = d->sws_dst_h = 0;
    d->sws_src_fmt = AV_PIX_FMT_NONE;
    return d;
}

PVDecoder *pv_decoder_open(const char *input_path) {
    return pv_decoder_open_with_input_format(input_path, NULL, NULL);
}

void pv_decoder_close(PVDecoder *d) {
    if (!d) return;
    if (d->sws) sws_freeContext(d->sws);
    if (d->frame) av_frame_free(&d->frame);
    if (d->pkt) av_packet_free(&d->pkt);
    if (d->dec) avcodec_free_context(&d->dec);
    if (d->fmt) avformat_close_input(&d->fmt);
    free(d);
}

static int scale_frame_to_rgba(PVDecoder *d, AVFrame *src, int target_width, PVThumb *out) {
    if (!d || !src || !out) return AVERROR(EINVAL);
    memset(out, 0, sizeof(*out));

    int src_w = src->width > 0 ? src->width : d->dec->width;
    int src_h = src->height > 0 ? src->height : d->dec->height;
    if (src_w <= 0 || src_h <= 0) {
        set_error("invalid source dimensions %dx%d", src_w, src_h);
        return AVERROR(EINVAL);
    }
    if (target_width <= 0) target_width = 160;
    int dst_w = target_width;
    int dst_h = (int)lrint((double)src_h * (double)dst_w / (double)src_w);
    if (dst_h <= 0) dst_h = 1;

    d->sws = sws_getCachedContext(
        d->sws,
        src_w, src_h, (enum AVPixelFormat)src->format,
        dst_w, dst_h, AV_PIX_FMT_RGBA,
        SWS_FAST_BILINEAR,
        NULL, NULL, NULL
    );
    if (!d->sws) {
        set_error("sws_getCachedContext failed");
        return AVERROR(EINVAL);
    }

    int linesize[4] = {0};
    uint8_t *dst_data[4] = {0};
    int buf_size = av_image_alloc(dst_data, linesize, dst_w, dst_h, AV_PIX_FMT_RGBA, 1);
    if (buf_size < 0) { set_av_error("av_image_alloc", buf_size); return buf_size; }

    int scaled = sws_scale(d->sws, (const uint8_t * const *)src->data, src->linesize, 0, src_h, dst_data, linesize);
    if (scaled <= 0) {
        av_freep(&dst_data[0]);
        set_error("sws_scale failed");
        return AVERROR(EINVAL);
    }

    out->width = dst_w;
    out->height = dst_h;
    out->stride = linesize[0];
    out->rgba_size = buf_size;
    out->rgba = dst_data[0];
    return 0;
}

int pv_seek_thumbnail(PVDecoder *d, double seconds, int target_width, PVThumb *out) {
    if (!d || !out) {
        set_error("pv_seek_thumbnail: nil decoder/out");
        return AVERROR(EINVAL);
    }
    memset(out, 0, sizeof(*out));

    if (seconds < 0) seconds = 0;
    int64_t target_ts = av_rescale_q((int64_t)llround(seconds * (double)AV_TIME_BASE), AV_TIME_BASE_Q, d->stream->time_base);

    int ret = avformat_seek_file(d->fmt, d->stream_index, INT64_MIN, target_ts, target_ts, AVSEEK_FLAG_BACKWARD);
    if (ret < 0) {
        // Some demuxers dislike max==target. Retry with a wider upper bound.
        ret = avformat_seek_file(d->fmt, d->stream_index, INT64_MIN, target_ts, INT64_MAX, AVSEEK_FLAG_BACKWARD);
        if (ret < 0) { set_av_error("avformat_seek_file", ret); return ret; }
    }
    avcodec_flush_buffers(d->dec);

    int packets = 0;
    AVFrame *fallback = NULL;

    while ((ret = av_read_frame(d->fmt, d->pkt)) >= 0) {
        packets++;
        if (packets > MAX_DECODE_PACKETS) {
            av_packet_unref(d->pkt);
            set_error("decode packet limit reached while seeking %.3fs", seconds);
            ret = AVERROR(EAGAIN);
            goto fail;
        }
        if (d->pkt->stream_index != d->stream_index) {
            av_packet_unref(d->pkt);
            continue;
        }

        ret = avcodec_send_packet(d->dec, d->pkt);
        av_packet_unref(d->pkt);
        if (ret < 0 && ret != AVERROR(EAGAIN)) { set_av_error("avcodec_send_packet", ret); goto fail; }

        while ((ret = avcodec_receive_frame(d->dec, d->frame)) >= 0) {
            int64_t pts = d->frame->best_effort_timestamp;
            if (pts == AV_NOPTS_VALUE || pts >= target_ts) {
                int sr = scale_frame_to_rgba(d, d->frame, target_width, out);
                av_frame_unref(d->frame);
                av_frame_free(&fallback);
                return sr;
            }
            // Keep exactly one early decoded frame as EOF fallback. Do this only
            // after checking the target match so the common path avoids cloning.
            if (!fallback) {
                fallback = av_frame_clone(d->frame);
                if (!fallback) {
                    av_frame_unref(d->frame);
                    set_error("av_frame_clone fallback failed");
                    ret = AVERROR(ENOMEM);
                    goto fail;
                }
            }
            av_frame_unref(d->frame);
        }
        if (ret != AVERROR(EAGAIN) && ret != AVERROR_EOF) { set_av_error("avcodec_receive_frame", ret); goto fail; }
    }

    if (fallback) {
        int sr = scale_frame_to_rgba(d, fallback, target_width, out);
        av_frame_free(&fallback);
        return sr;
    }

    if (ret == AVERROR_EOF) set_error("EOF before thumbnail at %.3fs", seconds);
    else set_av_error("av_read_frame", ret);
    return ret < 0 ? ret : AVERROR(EINVAL);

fail:
    if (fallback) av_frame_free(&fallback);
    av_packet_unref(d->pkt);
    av_frame_unref(d->frame);
    return ret < 0 ? ret : AVERROR(EINVAL);
}

void pv_thumb_free(PVThumb *t) {
    if (!t) return;
    if (t->rgba) av_freep(&t->rgba);
    t->width = t->height = t->stride = t->rgba_size = 0;
}


static void close_input(AVFormatContext **fmt) {
    if (fmt && *fmt) avformat_close_input(fmt);
}

static int open_video_input(const char *input_path, AVFormatContext **fmt_out, int *video_index_out) {
    if (!input_path || !fmt_out || !video_index_out) return AVERROR(EINVAL);
    *fmt_out = NULL;
    *video_index_out = -1;
    pv_ffmpeg_init();

    AVFormatContext *fmt = NULL;
    fmt = avformat_alloc_context();
    if (!fmt) { set_error("avformat_alloc_context failed"); return AVERROR(ENOMEM); }
    fmt->flags |= AVFMT_FLAG_GENPTS;

    int ret = avformat_open_input(&fmt, input_path, NULL, NULL);
    if (ret < 0) { set_av_error("avformat_open_input", ret); avformat_free_context(fmt); return ret; }
    ret = avformat_find_stream_info(fmt, NULL);
    if (ret < 0) { set_av_error("avformat_find_stream_info", ret); avformat_close_input(&fmt); return ret; }
    int idx = av_find_best_stream(fmt, AVMEDIA_TYPE_VIDEO, -1, -1, NULL, 0);
    if (idx < 0) { set_av_error("av_find_best_stream(video)", idx); avformat_close_input(&fmt); return idx; }
    *fmt_out = fmt;
    *video_index_out = idx;
    return 0;
}

static int open_video_output_copy(const char *output_path, AVFormatContext *ifmt, int in_video_index, AVFormatContext **ofmt_out, AVStream **out_stream_out, int faststart) {
    if (!output_path || !ifmt || !ofmt_out || !out_stream_out) return AVERROR(EINVAL);
    *ofmt_out = NULL;
    *out_stream_out = NULL;

    AVFormatContext *ofmt = NULL;
    int ret = avformat_alloc_output_context2(&ofmt, NULL, NULL, output_path);
    if (ret < 0 || !ofmt) { set_av_error("avformat_alloc_output_context2", ret); return ret < 0 ? ret : AVERROR(EINVAL); }

    AVStream *in_st = ifmt->streams[in_video_index];
    AVStream *out_st = avformat_new_stream(ofmt, NULL);
    if (!out_st) { set_error("avformat_new_stream failed"); avformat_free_context(ofmt); return AVERROR(ENOMEM); }

    ret = avcodec_parameters_copy(out_st->codecpar, in_st->codecpar);
    if (ret < 0) { set_av_error("avcodec_parameters_copy", ret); avformat_free_context(ofmt); return ret; }
    out_st->codecpar->codec_tag = 0;
    out_st->time_base = in_st->time_base;
    ofmt->avoid_negative_ts = AVFMT_AVOID_NEG_TS_DISABLED;

    if (!(ofmt->oformat->flags & AVFMT_NOFILE)) {
        ret = avio_open(&ofmt->pb, output_path, AVIO_FLAG_WRITE);
        if (ret < 0) { set_av_error("avio_open output", ret); avformat_free_context(ofmt); return ret; }
    }

    AVDictionary *mux_opts = NULL;
    if (faststart) av_dict_set(&mux_opts, "movflags", "+faststart", 0);
    ret = avformat_write_header(ofmt, &mux_opts);
    av_dict_free(&mux_opts);
    if (ret < 0) {
        set_av_error("avformat_write_header", ret);
        if (!(ofmt->oformat->flags & AVFMT_NOFILE)) avio_closep(&ofmt->pb);
        avformat_free_context(ofmt);
        return ret;
    }

    *ofmt_out = ofmt;
    *out_stream_out = out_st;
    return 0;
}

static void close_output(AVFormatContext **ofmt) {
    if (!ofmt || !*ofmt) return;
    if (!((*ofmt)->oformat->flags & AVFMT_NOFILE)) avio_closep(&(*ofmt)->pb);
    avformat_free_context(*ofmt);
    *ofmt = NULL;
}

int pv_copy_video_slice_meta(const char *input_path, const char *output_path, double start_seconds, double slice_seconds, PVPreviewSliceMeta *meta) {
    if (!input_path || !output_path) { set_error("pv_copy_video_slice: nil path"); return AVERROR(EINVAL); }
    if (start_seconds < 0) start_seconds = 0;
    if (slice_seconds <= 0) slice_seconds = 2.5;
    if (meta) {
        memset(meta, 0, sizeof(*meta));
        meta->requested_start = start_seconds;
    }

    AVFormatContext *ifmt = NULL;
    int video_idx = -1;
    int ret = open_video_input(input_path, &ifmt, &video_idx);
    if (ret < 0) return ret;
    AVStream *in_st = ifmt->streams[video_idx];

    int64_t target_ts = av_rescale_q((int64_t)llround(start_seconds * (double)AV_TIME_BASE), AV_TIME_BASE_Q, in_st->time_base);
    int64_t requested_duration_ts = av_rescale_q((int64_t)llround(slice_seconds * (double)AV_TIME_BASE), AV_TIME_BASE_Q, in_st->time_base);
    if (requested_duration_ts <= 0) requested_duration_ts = 1;
    int64_t end_ts = target_ts + requested_duration_ts;

    // Match the intended CLI shape: ffmpeg -ss <start> -i input -t <duration> -c copy part.mp4.
    // For stream copy, FFmpeg seeks to an earlier keyframe, writes pre-roll with
    // negative timestamps/discard flags, and lets the MP4 edit list make the part
    // present as roughly slice_seconds long. This keeps cutting fast while making
    // the final concat/transcode behave like the CLI flow.
    int64_t target_us = (int64_t)llround(start_seconds * (double)AV_TIME_BASE);
    ret = av_seek_frame(ifmt, -1, target_us, AVSEEK_FLAG_BACKWARD);
    if (ret < 0) ret = avformat_seek_file(ifmt, video_idx, INT64_MIN, target_ts, target_ts, AVSEEK_FLAG_BACKWARD);
    if (ret < 0) { set_av_error("slice av_seek_frame", ret); close_input(&ifmt); return ret; }

    AVFormatContext *ofmt = NULL;
    AVStream *out_st = NULL;
    ret = open_video_output_copy(output_path, ifmt, video_idx, &ofmt, &out_st, 0);
    if (ret < 0) { close_input(&ifmt); return ret; }

    AVPacket *pkt = av_packet_alloc();
    if (!pkt) { set_error("av_packet_alloc failed"); close_output(&ofmt); close_input(&ifmt); return AVERROR(ENOMEM); }

    int wrote = 0;
    int64_t first_ref = AV_NOPTS_VALUE;
    int64_t last_ref = AV_NOPTS_VALUE;
    int64_t last_dts_out = AV_NOPTS_VALUE;

    while ((ret = av_read_frame(ifmt, pkt)) >= 0) {
        if (pkt->stream_index != video_idx) { av_packet_unref(pkt); continue; }

        int64_t pkt_ref = pkt->dts != AV_NOPTS_VALUE ? pkt->dts : pkt->pts;
        if (pkt_ref == AV_NOPTS_VALUE) pkt_ref = last_ref == AV_NOPTS_VALUE ? target_ts : last_ref;
        if (wrote && pkt_ref >= end_ts) { av_packet_unref(pkt); break; }
        if (first_ref == AV_NOPTS_VALUE) first_ref = pkt_ref;

        if (pkt_ref < target_ts) pkt->flags |= AV_PKT_FLAG_DISCARD;

        if (pkt->pts != AV_NOPTS_VALUE) pkt->pts -= target_ts;
        if (pkt->dts != AV_NOPTS_VALUE) pkt->dts -= target_ts;
        if (pkt->dts != AV_NOPTS_VALUE) {
            if (last_dts_out != AV_NOPTS_VALUE && pkt->dts <= last_dts_out) {
                int64_t delta = last_dts_out + 1 - pkt->dts;
                pkt->dts += delta;
                if (pkt->pts != AV_NOPTS_VALUE) pkt->pts += delta;
            }
            if (pkt->pts != AV_NOPTS_VALUE && pkt->pts < pkt->dts) pkt->pts = pkt->dts;
            last_dts_out = pkt->dts;
        }

        av_packet_rescale_ts(pkt, in_st->time_base, out_st->time_base);
        pkt->stream_index = out_st->index;
        pkt->pos = -1;
        ret = av_interleaved_write_frame(ofmt, pkt);
        av_packet_unref(pkt);
        if (ret < 0) { set_av_error("slice av_interleaved_write_frame", ret); av_packet_free(&pkt); close_output(&ofmt); close_input(&ifmt); return ret; }
        wrote = 1;
        last_ref = pkt_ref;
    }

    if (ret < 0 && ret != AVERROR_EOF) { set_av_error("slice av_read_frame", ret); av_packet_free(&pkt); close_output(&ofmt); close_input(&ifmt); return ret; }
    av_packet_free(&pkt);

    if (!wrote || first_ref == AV_NOPTS_VALUE) { set_error("slice wrote no packets at %.3fs", start_seconds); close_output(&ofmt); close_input(&ifmt); return AVERROR(EIO); }

    ret = av_write_trailer(ofmt);
    if (ret < 0) { set_av_error("slice av_write_trailer", ret); close_output(&ofmt); close_input(&ifmt); return ret; }

    if (meta) {
        double tb = av_q2d(in_st->time_base);
        double actual_start = (double)first_ref * tb;
        double copied_duration = last_ref != AV_NOPTS_VALUE && last_ref >= target_ts ? ((double)(last_ref - target_ts) * tb) : slice_seconds;
        meta->requested_start = start_seconds;
        meta->actual_start = actual_start;
        meta->inpoint = 0.0;
        meta->outpoint = 0.0;
        meta->copied_duration = copied_duration;
    }

    close_output(&ofmt);
    close_input(&ifmt);
    return 0;
}

int pv_copy_video_slice(const char *input_path, const char *output_path, double start_seconds, double slice_seconds) {
    return pv_copy_video_slice_meta(input_path, output_path, start_seconds, slice_seconds, NULL);
}

int pv_concat_video_segments(const char *list_path, const char *output_path) {
    if (!list_path || !output_path) { set_error("pv_concat_video_segments: nil path"); return AVERROR(EINVAL); }
    pv_ffmpeg_init();

    const AVInputFormat *concat_fmt = av_find_input_format("concat");
    if (!concat_fmt) { set_error("concat demuxer not available in this FFmpeg build"); return AVERROR_DEMUXER_NOT_FOUND; }

    AVFormatContext *ifmt = NULL;
    AVDictionary *opts = NULL;
    av_dict_set(&opts, "safe", "0", 0);
    av_dict_set(&opts, "auto_convert", "1", 0);
    int ret = avformat_open_input(&ifmt, list_path, concat_fmt, &opts);
    av_dict_free(&opts);
    if (ret < 0) { set_av_error("concat avformat_open_input", ret); return ret; }

    ret = avformat_find_stream_info(ifmt, NULL);
    if (ret < 0) { set_av_error("concat avformat_find_stream_info", ret); close_input(&ifmt); return ret; }

    int video_idx = av_find_best_stream(ifmt, AVMEDIA_TYPE_VIDEO, -1, -1, NULL, 0);
    if (video_idx < 0) { set_av_error("concat av_find_best_stream(video)", video_idx); close_input(&ifmt); return video_idx; }

    AVFormatContext *ofmt = NULL;
    AVStream *out_st = NULL;
    ret = open_video_output_copy(output_path, ifmt, video_idx, &ofmt, &out_st, 1);
    if (ret < 0) { close_input(&ifmt); return ret; }

    AVStream *in_st = ifmt->streams[video_idx];
    AVPacket *pkt = av_packet_alloc();
    if (!pkt) { set_error("av_packet_alloc concat failed"); close_output(&ofmt); close_input(&ifmt); return AVERROR(ENOMEM); }

    int64_t last_dts = AV_NOPTS_VALUE;
    while ((ret = av_read_frame(ifmt, pkt)) >= 0) {
        if (pkt->stream_index != video_idx) { av_packet_unref(pkt); continue; }

        av_packet_rescale_ts(pkt, in_st->time_base, out_st->time_base);
        pkt->stream_index = out_st->index;
        pkt->pos = -1;
        if (pkt->dts != AV_NOPTS_VALUE) {
            if (last_dts != AV_NOPTS_VALUE && pkt->dts <= last_dts) {
                int64_t delta = last_dts + 1 - pkt->dts;
                pkt->dts += delta;
                if (pkt->pts != AV_NOPTS_VALUE) pkt->pts += delta;
            }
            last_dts = pkt->dts;
        }

        ret = av_interleaved_write_frame(ofmt, pkt);
        av_packet_unref(pkt);
        if (ret < 0) { set_av_error("concat av_interleaved_write_frame", ret); av_packet_free(&pkt); close_output(&ofmt); close_input(&ifmt); return ret; }
    }
    if (ret < 0 && ret != AVERROR_EOF) { set_av_error("concat av_read_frame", ret); av_packet_free(&pkt); close_output(&ofmt); close_input(&ifmt); return ret; }
    av_packet_free(&pkt);

    ret = av_write_trailer(ofmt);
    if (ret < 0) { set_av_error("concat av_write_trailer", ret); close_output(&ofmt); close_input(&ifmt); return ret; }

    close_output(&ofmt);
    close_input(&ifmt);
    return 0;
}


static const AVCodec *choose_video_encoder(enum AVCodecID *codec_id) {
    const AVCodec *codec = avcodec_find_encoder_by_name("libx264");
    if (codec) { *codec_id = codec->id; return codec; }
    codec = avcodec_find_encoder(AV_CODEC_ID_H264);
    if (codec) { *codec_id = AV_CODEC_ID_H264; return codec; }
    codec = avcodec_find_encoder(AV_CODEC_ID_MPEG4);
    if (codec) { *codec_id = AV_CODEC_ID_MPEG4; return codec; }
    *codec_id = AV_CODEC_ID_NONE;
    return NULL;
}

static int encode_video_frame(AVFormatContext *ofmt, AVCodecContext *enc, AVStream *out_st, AVPacket *pkt, AVFrame *frame) {
    int ret = avcodec_send_frame(enc, frame);
    if (ret < 0) { set_av_error("avcodec_send_frame", ret); return ret; }

    while ((ret = avcodec_receive_packet(enc, pkt)) >= 0) {
        if (pkt->duration <= 0) pkt->duration = 1;
        av_packet_rescale_ts(pkt, enc->time_base, out_st->time_base);
        pkt->stream_index = out_st->index;
        pkt->pos = -1;
        ret = av_interleaved_write_frame(ofmt, pkt);
        av_packet_unref(pkt);
        if (ret < 0) { set_av_error("encode av_interleaved_write_frame", ret); return ret; }
    }

    if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) return 0;
    set_av_error("avcodec_receive_packet", ret);
    return ret;
}

static int drain_filter_to_encoder(
    AVFilterContext *sink_ctx,
    AVRational sink_tb,
    AVFormatContext *ofmt,
    AVCodecContext *enc,
    AVStream *out_st,
    AVPacket *enc_pkt,
    AVFrame *filt_frame,
    int64_t *first_pts,
    int64_t *last_pts,
    int64_t *fallback_pts
) {
    int ret = 0;
    while ((ret = av_buffersink_get_frame(sink_ctx, filt_frame)) >= 0) {
        int64_t out_pts;
        if (filt_frame->pts != AV_NOPTS_VALUE) {
            if (*first_pts == AV_NOPTS_VALUE) *first_pts = filt_frame->pts;
            out_pts = av_rescale_q(filt_frame->pts - *first_pts, sink_tb, enc->time_base);
        } else {
            out_pts = (*fallback_pts)++;
        }
        if (*last_pts != AV_NOPTS_VALUE && out_pts <= *last_pts) {
            out_pts = *last_pts + 1;
        }
        filt_frame->pts = out_pts;
        filt_frame->duration = 1;
        filt_frame->pict_type = AV_PICTURE_TYPE_NONE;
        filt_frame->flags &= ~AV_FRAME_FLAG_KEY;
        *last_pts = out_pts;
        ret = encode_video_frame(ofmt, enc, out_st, enc_pkt, filt_frame);
        av_frame_unref(filt_frame);
        if (ret < 0) return ret;
    }
    if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) return 0;
    set_av_error("filter av_buffersink_get_frame", ret);
    return ret;
}

static int init_video_filter(
    PVDecoder *dec,
    int target_width,
    AVRational fps,
    AVFilterGraph **graph_out,
    AVFilterContext **src_out,
    AVFilterContext **sink_out,
    int *out_w,
    int *out_h,
    AVRational *sink_tb
) {
    int ret = 0;
    char args[512];
    char filter_descr[256];
    const AVFilter *buffersrc = avfilter_get_by_name("buffer");
    const AVFilter *buffersink = avfilter_get_by_name("buffersink");
    AVFilterInOut *outputs = avfilter_inout_alloc();
    AVFilterInOut *inputs = avfilter_inout_alloc();
    AVFilterGraph *graph = avfilter_graph_alloc();
    AVFilterContext *src_ctx = NULL;
    AVFilterContext *sink_ctx = NULL;
    if (!outputs || !inputs || !graph) {
        set_error("filter graph allocation failed");
        ret = AVERROR(ENOMEM);
        goto fail;
    }
    if (!buffersrc || !buffersink) {
        set_error("required buffer/buffersink filters are not available");
        ret = AVERROR_FILTER_NOT_FOUND;
        goto fail;
    }

    AVRational sar = dec->stream->sample_aspect_ratio.num > 0 ? dec->stream->sample_aspect_ratio : dec->dec->sample_aspect_ratio;
    if (sar.num <= 0 || sar.den <= 0) sar = (AVRational){1, 1};
    AVRational tb = dec->stream->time_base;
    if (tb.num <= 0 || tb.den <= 0) tb = (AVRational){1, 1000000};

    snprintf(args, sizeof(args),
        "video_size=%dx%d:pix_fmt=%d:time_base=%d/%d:pixel_aspect=%d/%d",
        dec->dec->width, dec->dec->height, dec->dec->pix_fmt,
        tb.num, tb.den,
        sar.num, sar.den
    );
    ret = avfilter_graph_create_filter(&src_ctx, buffersrc, "in", args, NULL, graph);
    if (ret < 0) { set_av_error("avfilter_graph_create_filter buffer", ret); goto fail; }
    ret = avfilter_graph_create_filter(&sink_ctx, buffersink, "out", NULL, NULL, graph);
    if (ret < 0) { set_av_error("avfilter_graph_create_filter buffersink", ret); goto fail; }


    if (target_width > 0) {
        snprintf(filter_descr, sizeof(filter_descr), "scale=%d:-2:flags=fast_bilinear,fps=%d/%d,format=yuv420p", target_width, fps.num, fps.den);
    } else {
        snprintf(filter_descr, sizeof(filter_descr), "fps=%d/%d,format=yuv420p", fps.num, fps.den);
    }

    outputs->name = av_strdup("in");
    outputs->filter_ctx = src_ctx;
    outputs->pad_idx = 0;
    outputs->next = NULL;
    inputs->name = av_strdup("out");
    inputs->filter_ctx = sink_ctx;
    inputs->pad_idx = 0;
    inputs->next = NULL;
    if (!outputs->name || !inputs->name) {
        set_error("av_strdup filter endpoint failed");
        ret = AVERROR(ENOMEM);
        goto fail;
    }

    ret = avfilter_graph_parse_ptr(graph, filter_descr, &inputs, &outputs, NULL);
    if (ret < 0) { set_av_error("avfilter_graph_parse_ptr", ret); goto fail; }
    ret = avfilter_graph_config(graph, NULL);
    if (ret < 0) { set_av_error("avfilter_graph_config", ret); goto fail; }

    *graph_out = graph;
    *src_out = src_ctx;
    *sink_out = sink_ctx;
    *out_w = av_buffersink_get_w(sink_ctx);
    *out_h = av_buffersink_get_h(sink_ctx);
    *sink_tb = av_buffersink_get_time_base(sink_ctx);
    avfilter_inout_free(&inputs);
    avfilter_inout_free(&outputs);
    return 0;

fail:
    avfilter_inout_free(&inputs);
    avfilter_inout_free(&outputs);
    if (graph) avfilter_graph_free(&graph);
    return ret < 0 ? ret : AVERROR(EINVAL);
}

static int transcode_decoder_to_video(PVDecoder *dec, const char *output_path, int target_width, double target_fps) {
    if (!dec || !output_path) { set_error("transcode_decoder_to_video: nil input"); if (dec) pv_decoder_close(dec); return AVERROR(EINVAL); }

    double fps_d = target_fps > 1.0 && target_fps <= 120.0 ? target_fps : stream_fps(dec->stream);
    if (fps_d <= 1.0 || fps_d > 120.0) fps_d = 30.0;
    AVRational fps = av_d2q(fps_d, 1001000);
    if (fps.num <= 0 || fps.den <= 0) fps = (AVRational){30, 1};

    int src_w = dec->dec->width;
    int src_h = dec->dec->height;
    if (src_w <= 0 || src_h <= 0) { set_error("invalid source size for transcode %dx%d", src_w, src_h); pv_decoder_close(dec); return AVERROR(EINVAL); }

    AVFilterGraph *filter_graph = NULL;
    AVFilterContext *src_ctx = NULL;
    AVFilterContext *sink_ctx = NULL;
    int out_w = 0, out_h = 0;
    AVRational sink_tb = (AVRational){1, fps.num > 0 ? fps.num : 30};
    int ret = init_video_filter(dec, target_width, fps, &filter_graph, &src_ctx, &sink_ctx, &out_w, &out_h, &sink_tb);
    if (ret < 0) { pv_decoder_close(dec); return ret; }
    if (out_w <= 0 || out_h <= 0) { set_error("invalid filter output size %dx%d", out_w, out_h); avfilter_graph_free(&filter_graph); pv_decoder_close(dec); return AVERROR(EINVAL); }
    if (out_w % 2) out_w++;
    if (out_h % 2) out_h++;

    AVFormatContext *ofmt = NULL;
    ret = avformat_alloc_output_context2(&ofmt, NULL, NULL, output_path);
    if (ret < 0 || !ofmt) { set_av_error("transcode avformat_alloc_output_context2", ret); avfilter_graph_free(&filter_graph); pv_decoder_close(dec); return ret < 0 ? ret : AVERROR(EINVAL); }

    enum AVCodecID codec_id;
    const AVCodec *encoder = choose_video_encoder(&codec_id);
    if (!encoder) { set_error("no usable H264/MPEG4 encoder found"); avformat_free_context(ofmt); avfilter_graph_free(&filter_graph); pv_decoder_close(dec); return AVERROR_ENCODER_NOT_FOUND; }

    AVStream *out_st = avformat_new_stream(ofmt, NULL);
    if (!out_st) { set_error("transcode avformat_new_stream failed"); avformat_free_context(ofmt); avfilter_graph_free(&filter_graph); pv_decoder_close(dec); return AVERROR(ENOMEM); }

    AVCodecContext *enc = avcodec_alloc_context3(encoder);
    if (!enc) { set_error("transcode avcodec_alloc_context3 encoder failed"); avformat_free_context(ofmt); avfilter_graph_free(&filter_graph); pv_decoder_close(dec); return AVERROR(ENOMEM); }

    enc->codec_id = codec_id;
    enc->codec_type = AVMEDIA_TYPE_VIDEO;
    enc->width = out_w;
    enc->height = out_h;
    enc->pix_fmt = AV_PIX_FMT_YUV420P;
    enc->time_base = av_inv_q(fps);
    enc->framerate = fps;
    enc->bit_rate = 0;
    enc->gop_size = 48;
    enc->max_b_frames = 0;
    enc->thread_count = 0;
    if (ofmt->oformat->flags & AVFMT_GLOBALHEADER) enc->flags |= AV_CODEC_FLAG_GLOBAL_HEADER;

    AVDictionary *enc_opts = NULL;
    if (strcmp(encoder->name, "libx264") == 0) {
        av_dict_set(&enc_opts, "preset", "ultrafast", 0);
        av_dict_set(&enc_opts, "crf", "28", 0);
    }
    ret = avcodec_open2(enc, encoder, &enc_opts);
    av_dict_free(&enc_opts);
    if (ret < 0) { set_av_error("transcode avcodec_open2 encoder", ret); avcodec_free_context(&enc); avformat_free_context(ofmt); avfilter_graph_free(&filter_graph); pv_decoder_close(dec); return ret; }

    ret = avcodec_parameters_from_context(out_st->codecpar, enc);
    if (ret < 0) { set_av_error("transcode avcodec_parameters_from_context", ret); avcodec_free_context(&enc); avformat_free_context(ofmt); avfilter_graph_free(&filter_graph); pv_decoder_close(dec); return ret; }
    out_st->time_base = enc->time_base;

    if (!(ofmt->oformat->flags & AVFMT_NOFILE)) {
        ret = avio_open(&ofmt->pb, output_path, AVIO_FLAG_WRITE);
        if (ret < 0) { set_av_error("transcode avio_open", ret); avcodec_free_context(&enc); avformat_free_context(ofmt); avfilter_graph_free(&filter_graph); pv_decoder_close(dec); return ret; }
    }

    AVDictionary *mux_opts = NULL;
    av_dict_set(&mux_opts, "movflags", "+faststart", 0);
    ret = avformat_write_header(ofmt, &mux_opts);
    av_dict_free(&mux_opts);
    if (ret < 0) { set_av_error("transcode avformat_write_header", ret); if (!(ofmt->oformat->flags & AVFMT_NOFILE)) avio_closep(&ofmt->pb); avcodec_free_context(&enc); avformat_free_context(ofmt); avfilter_graph_free(&filter_graph); pv_decoder_close(dec); return ret; }

    AVPacket *enc_pkt = av_packet_alloc();
    AVFrame *filt_frame = av_frame_alloc();
    if (!enc_pkt || !filt_frame) { set_error("transcode encoder packet/filter frame alloc failed"); ret = AVERROR(ENOMEM); goto transcode_fail; }

    int64_t first_filter_pts = AV_NOPTS_VALUE;
    int64_t last_out_pts = AV_NOPTS_VALUE;
    int64_t fallback_pts = 0;

    while ((ret = av_read_frame(dec->fmt, dec->pkt)) >= 0) {
        if (dec->pkt->stream_index != dec->stream_index) { av_packet_unref(dec->pkt); continue; }
        ret = avcodec_send_packet(dec->dec, dec->pkt);
        av_packet_unref(dec->pkt);
        if (ret < 0 && ret != AVERROR(EAGAIN)) { set_av_error("transcode avcodec_send_packet", ret); goto transcode_fail; }

        while ((ret = avcodec_receive_frame(dec->dec, dec->frame)) >= 0) {
            if (dec->frame->best_effort_timestamp != AV_NOPTS_VALUE) dec->frame->pts = dec->frame->best_effort_timestamp;
            ret = av_buffersrc_add_frame_flags(src_ctx, dec->frame, AV_BUFFERSRC_FLAG_KEEP_REF);
            av_frame_unref(dec->frame);
            if (ret < 0) { set_av_error("filter av_buffersrc_add_frame", ret); goto transcode_fail; }
            ret = drain_filter_to_encoder(sink_ctx, sink_tb, ofmt, enc, out_st, enc_pkt, filt_frame, &first_filter_pts, &last_out_pts, &fallback_pts);
            if (ret < 0) goto transcode_fail;
        }
        if (ret != AVERROR(EAGAIN) && ret != AVERROR_EOF) { set_av_error("transcode avcodec_receive_frame", ret); goto transcode_fail; }
    }
    if (ret < 0 && ret != AVERROR_EOF) { set_av_error("transcode av_read_frame", ret); goto transcode_fail; }

    ret = avcodec_send_packet(dec->dec, NULL);
    if (ret >= 0) {
        while ((ret = avcodec_receive_frame(dec->dec, dec->frame)) >= 0) {
            if (dec->frame->best_effort_timestamp != AV_NOPTS_VALUE) dec->frame->pts = dec->frame->best_effort_timestamp;
            ret = av_buffersrc_add_frame_flags(src_ctx, dec->frame, AV_BUFFERSRC_FLAG_KEEP_REF);
            av_frame_unref(dec->frame);
            if (ret < 0) { set_av_error("filter flush av_buffersrc_add_frame", ret); goto transcode_fail; }
            ret = drain_filter_to_encoder(sink_ctx, sink_tb, ofmt, enc, out_st, enc_pkt, filt_frame, &first_filter_pts, &last_out_pts, &fallback_pts);
            if (ret < 0) goto transcode_fail;
        }
        if (ret != AVERROR_EOF && ret != AVERROR(EAGAIN)) { set_av_error("transcode flush avcodec_receive_frame", ret); goto transcode_fail; }
    }

    ret = av_buffersrc_add_frame_flags(src_ctx, NULL, 0);
    if (ret < 0) { set_av_error("filter av_buffersrc_add_frame NULL", ret); goto transcode_fail; }
    ret = drain_filter_to_encoder(sink_ctx, sink_tb, ofmt, enc, out_st, enc_pkt, filt_frame, &first_filter_pts, &last_out_pts, &fallback_pts);
    if (ret < 0) goto transcode_fail;

    ret = encode_video_frame(ofmt, enc, out_st, enc_pkt, NULL);
    if (ret < 0) goto transcode_fail;
    ret = av_write_trailer(ofmt);
    if (ret < 0) { set_av_error("transcode av_write_trailer", ret); goto transcode_fail; }

    if (enc_pkt) av_packet_free(&enc_pkt);
    if (filt_frame) av_frame_free(&filt_frame);
    avfilter_graph_free(&filter_graph);
    if (!(ofmt->oformat->flags & AVFMT_NOFILE)) avio_closep(&ofmt->pb);
    avcodec_free_context(&enc);
    avformat_free_context(ofmt);
    pv_decoder_close(dec);
    return 0;

transcode_fail:
    if (enc_pkt) av_packet_free(&enc_pkt);
    if (filt_frame) av_frame_free(&filt_frame);
    if (filter_graph) avfilter_graph_free(&filter_graph);
    if (ofmt && !(ofmt->oformat->flags & AVFMT_NOFILE)) avio_closep(&ofmt->pb);
    if (enc) avcodec_free_context(&enc);
    if (ofmt) avformat_free_context(ofmt);
    pv_decoder_close(dec);
    return ret < 0 ? ret : AVERROR(EINVAL);
}

int pv_transcode_video_resize(const char *input_path, const char *output_path, int target_width) {
    if (!input_path || !output_path) { set_error("pv_transcode_video_resize: nil path"); return AVERROR(EINVAL); }
    PVDecoder *dec = pv_decoder_open(input_path);
    if (!dec) return AVERROR(EINVAL);
    return transcode_decoder_to_video(dec, output_path, target_width, 0.0);
}

int pv_transcode_concat_video(const char *list_path, const char *output_path, int target_width, double target_fps) {
    if (!list_path || !output_path) { set_error("pv_transcode_concat_video: nil path"); return AVERROR(EINVAL); }
    pv_ffmpeg_init();
    const AVInputFormat *concat_fmt = av_find_input_format("concat");
    if (!concat_fmt) { set_error("concat demuxer not available in this FFmpeg build"); return AVERROR_DEMUXER_NOT_FOUND; }
    AVDictionary *opts = NULL;
    av_dict_set(&opts, "safe", "0", 0);
    av_dict_set(&opts, "auto_convert", "1", 0);
    PVDecoder *dec = pv_decoder_open_with_input_format(list_path, concat_fmt, &opts);
    av_dict_free(&opts);
    if (!dec) return AVERROR(EINVAL);
    return transcode_decoder_to_video(dec, output_path, target_width, target_fps);
}
