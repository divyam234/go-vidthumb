#!/usr/bin/env bash
set -euo pipefail

FFMPEG_PREFIX="${FFMPEG_PREFIX:-/mnt/data/ffmpeg-8.1}"
BIN="${BIN:-previewer}"

export PKG_CONFIG_PATH="$FFMPEG_PREFIX/lib/pkgconfig${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}"
# Use DT_RPATH rather than DT_RUNPATH so transitive FFmpeg shared-library deps
# from the same uploaded build are resolved without LD_LIBRARY_PATH.
export CGO_LDFLAGS="${CGO_LDFLAGS:-} -Wl,--disable-new-dtags -Wl,-rpath,$FFMPEG_PREFIX/lib"

CGO_ENABLED=1 go build -trimpath -o "$BIN" ./cmd/previewer
