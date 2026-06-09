package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	previewer "go-vidthumb"

	flag "github.com/spf13/pflag"
)

const appVersion = "dev"

func main() {
	fs := flag.NewFlagSet("previewer", flag.ExitOnError)
	fs.SortFlags = false

	input := fs.StringP("input", "i", "", "input media file")
	outDir := fs.StringP("out", "o", "out", "output directory")
	workers := fs.IntP("workers", "j", min(max(runtime.NumCPU()/2, 1), 4), "parallel seek workers")

	cols := fs.Int("cols", 8, "sprite columns")
	rows := fs.Int("rows", 5, "sprite rows")
	thumbWidth := fs.Int("thumb-width", 160, "thumbnail width in pixels")
	jpegQuality := fs.Int("jpeg-quality", 82, "sprite JPEG quality, 1-100")
	noSprite := fs.Bool("no-sprite", false, "skip sprite.jpg and sprite.vtt")

	previewSlices := fs.Int("preview-slices", 16, "number of preview video slices")
	sliceSeconds := fs.Float64("slice-seconds", 2.5, "seconds per preview slice")
	previewWidth := fs.Int("preview-width", 640, "final preview video width; 0 keeps source width")
	keepParts := fs.Bool("keep-parts", false, "keep copied preview slice files for verification/debugging")
	noPreview := fs.Bool("no-preview", false, "skip preview.mp4")

	version := fs.BoolP("version", "v", false, "print version and exit")

	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Generate seek-based media previews using FFmpeg libraries through cgo.\n\n")
		fmt.Fprintf(fs.Output(), "Usage:\n  %s --input video.mp4 --out out [flags]\n\n", os.Args[0])
		fmt.Fprintf(fs.Output(), "Flags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(os.Args[1:]); err != nil {
		fatal(err)
	}
	if *version {
		fmt.Println(appVersion)
		return
	}
	if *input == "" {
		fs.Usage()
		fatal(errors.New("missing --input"))
	}

	opts := previewer.DefaultOptions()
	opts.Workers = *workers
	opts.Sprite.Enabled = !*noSprite
	opts.Sprite.Columns = *cols
	opts.Sprite.Rows = *rows
	opts.Sprite.ThumbWidth = *thumbWidth
	opts.Sprite.JPEGQuality = *jpegQuality
	opts.Preview.Enabled = !*noPreview
	opts.Preview.Slices = *previewSlices
	opts.Preview.SliceSeconds = *sliceSeconds
	opts.Preview.Width = *previewWidth
	opts.Preview.KeepParts = *keepParts
	opts.Progress = func(e previewer.ProgressEvent) {
		if e.Total > 1 {
			fmt.Printf("%s: %d/%d\n", e.Stage, e.Done, e.Total)
		}
	}

	start := time.Now()
	res, err := previewer.Generate(context.Background(), previewer.FromFile(*input), previewer.OutputPaths{Dir: *outDir}, opts)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("input: %s\n", *input)
	fmt.Printf("probe: duration=%.3fs size=%dx%d fps=%.3f\n", res.Info.Duration, res.Info.Width, res.Info.Height, res.Info.FPS)
	if res.Sprite != nil {
		fmt.Printf("sprite: %s\n", res.Sprite.SpritePath)
		fmt.Printf("vtt:    %s\n", res.Sprite.VTTPath)
	}
	if res.Preview != nil {
		fmt.Printf("preview: %s\n", res.Preview.PreviewPath)
	}
	fmt.Printf("done in %s\n", time.Since(start).Round(time.Millisecond))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
