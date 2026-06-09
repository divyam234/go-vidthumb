# Go VidThumb

Go library + CLI for generating media preview assets and thumbnails with FFmpeg **shared libraries** through cgo.

The runtime code does **not** shell out to `ffmpeg` or `ffprobe`. FFmpeg CLI is used only in one regression test as a reference implementation.

## Features

- Reusable Go package, not just a command-line tool.
- Thin CLI wrapper built with [`spf13/pflag`](https://github.com/spf13/pflag).
- File input and `io.ReadSeeker` input.
- Parallel seek-based thumbnail extraction.
- Sprite JPEG + WebVTT thumbnail map generation.
- Preview MP4 generation from parallel stream-copy slices.
- Final preview transcode does resize/FPS normalization once, after concat.
- API-only perceptual hash calculation through `goimagehash`.
- Go-side image helpers through `imaging`.
- Tests for file vs reader parity, public API behavior, CLI smoke, race safety, RSS stress, and API vs real `ffmpeg` CLI decoded-frame parity.

## Output types

The public API is intentionally split into independent pieces:

| Need | API |
|---|---|
| Probe only | `ProbeFile`, `ProbeReadSeeker`, `ProbeSource` |
| Raw thumbnails only | `ExtractThumbnails`, `ExtractThumbnailsFromFile`, `ExtractThumbnailsFromReadSeeker` |
| Sprite only | `GenerateSprite`, `GenerateSpriteFromFile`, `GenerateSpriteFromReadSeeker` |
| Preview only | `GeneratePreview`, `GeneratePreviewFromFile`, `GeneratePreviewFromReadSeeker` |
| Preview + sprite convenience flow | `Generate`, `GenerateFromFile`, `GenerateFromReadSeeker` |
| pHash, API-only | `CalculatePHash`, `CalculatePHashFromFile`, `CalculatePHashFromReadSeeker`, `ComputePHashFromThumbnails` |

pHash is **not** exposed in the CLI and is not coupled to preview/sprite generation. It reuses the thumbnail extraction pipeline with pHash-specific options.

## FFmpeg dependency

You need FFmpeg shared libraries and headers discoverable by `pkg-config`:

- `libavformat`
- `libavcodec`
- `libavutil`
- `libswscale`
- `libswresample`

Example with the uploaded shared FFmpeg package used during development:

```bash
mkdir -p /mnt/data/ffmpeg-8.1
tar -xf /mnt/data/ffmpeg-n8.1-latest-linux64-gpl-shared-8.1.tar.xz \
  -C /mnt/data/ffmpeg-8.1 --strip-components=1

export FFMPEG_PREFIX=/mnt/data/ffmpeg-8.1
export PKG_CONFIG_PATH="$FFMPEG_PREFIX/lib/pkgconfig"
export CGO_LDFLAGS="-Wl,--disable-new-dtags -Wl,-rpath,$FFMPEG_PREFIX/lib"
```

Or build through the helper script:

```bash
FFMPEG_PREFIX=/mnt/data/ffmpeg-8.1 ./build.sh
```

## CLI usage

Build:

```bash
FFMPEG_PREFIX=/mnt/data/ffmpeg-8.1 ./build.sh
```

Generate preview + sprite:

```bash
./previewer \
  --input /videos/input.mp4 \
  --out /tmp/previewer-output \
  --workers 4 \
  --cols 8 \
  --rows 5 \
  --thumb-width 160 \
  --preview-slices 12 \
  --slice-seconds 2.5 \
  --preview-width 640
```

Useful flags:

```bash
--no-preview       # generate only sprite.jpg + sprite.vtt
--no-sprite        # generate only preview.mp4
--keep-parts       # keep .preview-parts/part_XXXX.mp4 for debugging
--preview-width 0  # keep source width in final preview
-i, --input        # input file
-o, --out          # output directory
-j, --workers      # parallel seek workers
```

Outputs:

```text
preview.mp4
sprite.jpg
sprite.vtt
```

The CLI intentionally does not expose pHash. Use the pHash API directly.

## Library usage

```go
package main

import (
    "context"
    "fmt"

    previewer "go-vidthumb"
)

func main() {
    opts := previewer.DefaultOptions()
    opts.Workers = 4
    opts.Sprite.Columns = 8
    opts.Sprite.Rows = 5
    opts.Sprite.ThumbWidth = 160
    opts.Preview.Slices = 12
    opts.Preview.SliceSeconds = 2.5
    opts.Preview.Width = 640

    res, err := previewer.GenerateFromFile(
        context.Background(),
        "/videos/input.mp4",
        previewer.OutputPaths{Dir: "/tmp/previewer-output"},
        opts,
    )
    if err != nil {
        panic(err)
    }

    fmt.Println(res.Preview.PreviewPath)
    fmt.Println(res.Sprite.SpritePath)
    fmt.Println(res.Sprite.VTTPath)
}
```

### `io.ReadSeeker` input

```go
data, err := os.ReadFile("/videos/input.mp4")
if err != nil {
    panic(err)
}

res, err := previewer.GenerateFromReadSeeker(
    context.Background(),
    "input.mp4",
    bytes.NewReader(data),
    previewer.OutputPaths{Dir: "/tmp/from-reader"},
    previewer.DefaultOptions(),
)
_ = res
_ = err
```

`io.ReadSeeker` input is spooled to a temporary file first. This is intentional: FFmpeg demuxing, seeking, MP4 stream-copy, and concat behavior stay identical to the direct file path backend. The tests verify byte-for-byte matching for slices, thumbnails, sprites, VTT, and full outputs from the same input bytes.

## Preview-only API

```go
preview, info, err := previewer.GeneratePreviewFromFile(
    context.Background(),
    "/videos/input.mp4",
    "/tmp/out/preview.mp4",
    previewer.PreviewOptions{
        Enabled:      true,
        Slices:       12,
        SliceSeconds: 2.5,
        Width:        640,
        KeepParts:    true,
    },
    4,
)
_ = info
_ = err
fmt.Println(preview.PreviewPath)
```

Preview behavior mirrors this FFmpeg CLI shape:

```bash
# Parallel stage, one command per slice:
ffmpeg -ss <start> -i input.mp4 -t <sliceSeconds> -an -c:v copy part_0000.mp4

# Final stage, once:
ffmpeg -f concat -safe 0 -i concat.txt \
  -vf "scale=640:-2:flags=fast_bilinear,fps=<source-fps>" \
  -an -c:v libx264 -preset ultrafast -crf 28 \
  -pix_fmt yuv420p -bf 0 -g 48 -movflags +faststart preview.mp4
```

The regression test compares the public API output against this real `ffmpeg` CLI reference by decoding both previews to raw `yuv420p` frames and hashing the decoded bytes. This avoids false failures from MP4 atom/metadata differences while still proving frame-for-frame equality.

## Sprite and thumbnails

Raw thumbnails only:

```go
thumbs, info, err := previewer.ExtractThumbnailsFromFile(
    context.Background(),
    "/videos/input.mp4",
    previewer.SpriteOptions{Columns: 8, Rows: 5, ThumbWidth: 160, JPEGQuality: 82},
    4,
)
_ = info
_ = err
fmt.Println(len(thumbs))
```

Sprite + VTT only:

```go
sprite, info, err := previewer.GenerateSpriteFromFile(
    context.Background(),
    "/videos/input.mp4",
    "/tmp/out/sprite.jpg",
    "/tmp/out/sprite.vtt",
    previewer.SpriteOptions{Columns: 8, Rows: 5, ThumbWidth: 160, JPEGQuality: 82},
    4,
)
_ = info
_ = err
fmt.Println(sprite.SpritePath)
```

Sprite generation:

1. Distributes `Columns * Rows` timestamps across the video duration.
2. Uses a Go worker pool.
3. Each worker owns its own FFmpeg demuxer/decoder context.
4. Each worker seeks, decodes one usable frame, scales to RGBA with `libswscale`, and returns bytes to Go.
5. Go composes `sprite.jpg` and writes `sprite.vtt`.

## pHash API

pHash is API-only and independent from CLI output generation.

```go
ph, info, err := previewer.CalculatePHashFromFile(
    context.Background(),
    "/videos/input.mp4",
    previewer.PHashOptions{
        Columns:    5,
        Rows:       5,
        ThumbWidth: 160,
        ResizeSize: 32,
        HashSize:   8,
    },
    4,
)
_ = info
_ = err
fmt.Println(ph.Hex)
```

If thumbnails are already available, avoid a second decode pass:

```go
hash, err := previewer.ComputePHashFromThumbnails(thumbs, previewer.PHashOptions{
    Columns:    5,
    Rows:       5,
    ThumbWidth: 160,
    ResizeSize: 32,
    HashSize:   8,
})
if err != nil {
    panic(err)
}
fmt.Println(previewer.FormatPHash(hash))
```


## Benchmarks: Go API vs FFmpeg 8.1 CLI

Two benchmark styles are included.

### Go benchmark tests

These are useful for quick CI/local tracking:

| Benchmark | What it measures |
|---|---|
| `BenchmarkPreviewPublicAPI` | Public `GeneratePreviewFromFile(...)`, including public source preparation/probe. |
| `BenchmarkPreviewLibraryCore` | Internal library preview pipeline with metadata already probed. |
| `BenchmarkPreviewFFmpegCLIReference` | Real `ffmpeg` CLI reference: slice commands plus final concat/scale/x264/faststart command. |

```bash
export FFMPEG_PREFIX=/mnt/data/ffmpeg-8.1
export PKG_CONFIG_PATH="$FFMPEG_PREFIX/lib/pkgconfig"
export CGO_LDFLAGS="-Wl,--disable-new-dtags -Wl,-rpath,$FFMPEG_PREFIX/lib"

# Use the uploaded FFmpeg 8.1 CLI, not the system ffmpeg.
export PREVIEWER_FFMPEG_CLI="$FFMPEG_PREFIX/bin/ffmpeg"
export LD_LIBRARY_PATH="$FFMPEG_PREFIX/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"

export PREVIEWER_BENCH_INPUT=/mnt/data/big-buck-bunny-1080p-30sec.mp4
export PREVIEWER_BENCH_SLICES=12
export PREVIEWER_BENCH_SLICE_SECONDS=2.5
export PREVIEWER_BENCH_WIDTH=640
export PREVIEWER_BENCH_WORKERS=4

go test -run '^$' \
  -bench 'BenchmarkPreview(PublicAPI|LibraryCore|FFmpegCLIReference)$' \
  -benchtime=1x \
  -count=1
```

### Real resource benchmark

For wall time, CPU time, max RSS, output size, and decoded-frame hash reporting, use `previewbench`:

```bash
go run ./cmd/previewbench \
  --input /mnt/data/big-buck-bunny-1080p-30sec.mp4 \
  --out /mnt/data/go-vidthumb-matchbench-output \
  --ffmpeg /mnt/data/ffmpeg-8.1/bin/ffmpeg \
  --workers 4 \
  --preview-slices 12 \
  --slice-seconds 2.5 \
  --preview-width 640
```

`previewbench` runs the Go API first and the FFmpeg CLI second, sequentially, so they do **not** compete for CPU or disk at the same time. The FFmpeg CLI path uses the uploaded FFmpeg 8.1 binary and runs it from its own `bin/` directory so its shared libraries resolve correctly.

Sandbox resource benchmark from the uploaded Big Buck Bunny 1080p 30s sample:

| Case | Wall | User CPU | Sys CPU | CPU/wall | Max RSS | Output | Commands |
|---|---:|---:|---:|---:|---:|---:|---:|
| Go API | `20.572s` | `22.97s` | `920ms` | `1.16x` | `256.3 MiB` | `7.1 MiB` | `1` |
| FFmpeg 8.1 CLI | `12.776s` | `48.82s` | `6.77s` | `4.35x` | `355.2 MiB` | `7.1 MiB` | `13` |

Interpretation for this sample:

- **FFmpeg 8.1 CLI is faster by wall time** for this workload.
- **Go API uses much less peak RSS** in this run.
- FFmpeg CLI consumes more total CPU because its final x264/filter stage uses many threads aggressively.
- `previewbench` also writes `benchmark-report.md` with raw decoded-frame hashes. On this full 30s sample, Go API and FFmpeg 8.1 CLI both decoded to `258163200` bytes and the same raw SHA-256: `82b923491cc19ed65ec25d0560c80a176e8145842e8988cc876e92c8b4a1d9ce`.

Environment for the above run:

```text
CPU: Intel(R) Xeon(R) Platinum 8370C CPU @ 2.80GHz
OS/arch: linux/amd64
Input: Big Buck Bunny 1080p, 30 seconds
Preview options: 12 slices × 2.5s, final width 640, 4 workers
Go API FFmpeg libs: uploaded FFmpeg 8.1 shared build
Reference CLI: uploaded FFmpeg 8.1 shared build, /mnt/data/ffmpeg-8.1/bin/ffmpeg
```

## Testing

Set FFmpeg library paths first:

```bash
export FFMPEG_PREFIX=/mnt/data/ffmpeg-8.1
export PKG_CONFIG_PATH="$FFMPEG_PREFIX/lib/pkgconfig"
export CGO_LDFLAGS="-Wl,--disable-new-dtags -Wl,-rpath,$FFMPEG_PREFIX/lib"
```

Run the full suite:

```bash
go test ./... -count=1
go test -race ./... -count=1
```

Optional larger-input smoke test:

```bash
PREVIEWER_TEST_INPUT=/mnt/data/big-buck-bunny-1080p-30sec.mp4 go test ./... -count=1
```

Optional RSS/leak stress check:

```bash
PREVIEWER_STRESS_TEST=1 go test -run TestRepeatedThumbnailExtractionRSSBounded -count=1 .
```

Test coverage includes:

- File input vs `io.ReadSeeker` parity.
- Preview slices are byte-identical between file and reader backends.
- Raw thumbnail bytes are identical between file and reader backends.
- Sprite JPEG and VTT are identical between file and reader backends.
- pHash values match between file and reader backends.
- `ComputePHashFromThumbnails` matches `CalculatePHash` when given the same thumbnails/options.
- Relative output directories do not break concat path resolution.
- Public preview API output matches real FFmpeg CLI decoded frames.
- CLI end-to-end smoke test.
- Race test.
- Optional RSS bounded stress test.

## Runtime architecture

### Preview

```text
parallel workers:
  seek timestamp
  stream-copy one short MP4 part, no resize, no encode

final stage:
  concat demuxer with safe=0
  decode once
  scale/fps normalize once
  encode one final MP4
  movflags=+faststart
```

### Sprite / pHash

```text
parallel workers:
  seek timestamp
  decode one frame
  scale to RGBA

sprite:
  compose JPEG + VTT in Go

pHash:
  compose in-memory montage
  call goimagehash.PerceptionHash
```

## Memory and cgo ownership

The cgo layer keeps FFmpeg ownership explicit:

- every worker owns its own `AVFormatContext`, `AVCodecContext`, packet, frame, and scaler context;
- every C thumbnail buffer is copied into Go memory and released with `pv_thumb_free`;
- fallback decoded frames are released on success and error paths;
- final preview transcode reuses one encoder packet;
- worker queues are buffered and drained to avoid blocked goroutines on errors;
- `io.ReadSeeker` temp files are removed after processing.

## Notes for publishing

The current module path is:

```go
module go-vidthumb
```

Before publishing publicly, change it to your repository path, for example:

```go
module github.com/yourname/go-vidthumb
```

Then update imports in examples and downstream apps.
