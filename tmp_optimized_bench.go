//go:build ignore

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	vidthumb "github.com/divyam234/go-vidthumb"
)

func main() {
	if len(os.Args) != 3 {
		panic("usage: bench input outdir")
	}
	input, outDir := os.Args[1], os.Args[2]
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		panic(err)
	}
	ctx := context.Background()

	spriteStart := time.Now()
	_, _, err := vidthumb.GenerateSpriteFromFile(ctx, input,
		filepath.Join(outDir, "sprite.jpg"), filepath.Join(outDir, "sprite.vtt"),
		vidthumb.SpriteOptions{Enabled: true, Columns: 10, Rows: 15, ThumbWidth: 480, JPEGQuality: 95, FastSeek: true}, 8)
	if err != nil {
		panic(err)
	}
	fmt.Printf("sprite=%s\n", time.Since(spriteStart))

	previewStart := time.Now()
	_, _, err = vidthumb.GeneratePreviewFromFile(ctx, input,
		filepath.Join(outDir, "preview.mp4"),
		vidthumb.PreviewOptions{Enabled: true, Slices: 30, SliceSeconds: 2, Width: 640, FPS: 15}, 8)
	if err != nil {
		panic(err)
	}
	fmt.Printf("preview=%s\n", time.Since(previewStart))

	phashStart := time.Now()
	result, _, err := vidthumb.CalculatePHashFromFile(ctx, input,
		vidthumb.PHashOptions{Columns: 5, Rows: 5, ThumbWidth: 160, HashSize: 8}, 8)
	if err != nil {
		panic(err)
	}
	fmt.Printf("phash=%s value=%s\n", time.Since(phashStart), result.Hex)
}
