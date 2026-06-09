package previewer

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"sort"
	"strings"

	goimagehash "github.com/corona10/goimagehash"
	"github.com/disintegration/imaging"
)

const (
	defaultPHashColumns    = 5
	defaultPHashRows       = 5
	defaultPHashThumbWidth = 160
	defaultPHashResizeSize = 32
	defaultPHashHashSize   = 8
)

func applyPHashDefaults(opts *PHashOptions) {
	if opts.Columns <= 0 {
		opts.Columns = defaultPHashColumns
	}
	if opts.Rows <= 0 {
		opts.Rows = defaultPHashRows
	}
	if opts.ThumbWidth <= 0 {
		opts.ThumbWidth = defaultPHashThumbWidth
	}
	if opts.ResizeSize <= 0 {
		opts.ResizeSize = defaultPHashResizeSize
	}
	if opts.HashSize <= 0 {
		opts.HashSize = defaultPHashHashSize
	}
}

// CalculatePHash samples video frames using the same seek-grid behavior as
// sprite generation, builds an in-memory montage, and calculates a perceptual
// hash from that montage. It is intentionally API-only and is not coupled to
// the CLI or Generate pipeline.
func CalculatePHash(ctx context.Context, src Source, opts PHashOptions, workers int) (*PHashResult, MediaInfo, error) {
	if workers <= 0 {
		workers = 1
	}
	applyPHashDefaults(&opts)

	thumbs, info, err := ExtractThumbnails(ctx, src, SpriteOptions{
		Columns:    opts.Columns,
		Rows:       opts.Rows,
		ThumbWidth: opts.ThumbWidth,
	}, workers)
	if err != nil {
		return nil, info, err
	}
	hash, err := ComputePHashFromThumbnails(thumbs, opts)
	if err != nil {
		return nil, info, err
	}
	return &PHashResult{Hash: hash, Hex: FormatPHash(hash), Thumbs: thumbs}, info, nil
}

// ComputePHashFromThumbnails calculates pHash from already extracted
// thumbnails. This is useful when callers already called ExtractThumbnails and
// want to avoid seeking/decoding twice. The implementation uses goimagehash for
// the DCT pHash and imaging for the final resize/normalization step.
func ComputePHashFromThumbnails(thumbs []Thumb, opts PHashOptions) (uint64, error) {
	if len(thumbs) == 0 {
		return 0, errors.New("no thumbnails")
	}
	applyPHashDefaults(&opts)
	montage, err := buildThumbMontage(thumbs, opts.Columns)
	if err != nil {
		return 0, err
	}
	return computePHashImageHash(montage, opts)
}

func computePHashImageHash(img image.Image, opts PHashOptions) (uint64, error) {
	if img == nil {
		return 0, errors.New("nil image")
	}
	applyPHashDefaults(&opts)

	// Keep this normalization explicit so callers can still tune options without
	// depending on goimagehash internals. goimagehash's normal 64-bit pHash path
	// does its own 64x64 resize; pre-resizing here is mostly useful for reducing
	// large montage cost and making the public options meaningful.
	resizeSize := opts.ResizeSize
	if resizeSize < 8 {
		resizeSize = 8
	}
	normalized := imaging.Resize(img, resizeSize, resizeSize, imaging.Linear)

	// Stash-style 64-bit pHash is the main API. goimagehash.PerceptionHash gives
	// that directly. For custom HashSize values that are powers of two when
	// squared, use goimagehash's extended hash and fold the words to a stable
	// uint64 so the existing PHashResult API remains backwards-compatible.
	if opts.HashSize == 8 {
		h, err := goimagehash.PerceptionHash(normalized)
		if err != nil {
			return 0, err
		}
		return h.GetHash(), nil
	}

	ext, err := goimagehash.ExtPerceptionHash(normalized, opts.HashSize, opts.HashSize)
	if err != nil {
		return 0, err
	}
	return foldHashWords(ext.GetHash()), nil
}

func foldHashWords(words []uint64) uint64 {
	var out uint64
	for i, w := range words {
		out ^= bits.RotateLeft64(w, (i%8)*8)
	}
	return out
}

func FormatPHash(hash uint64) string {
	return fmt.Sprintf("%016x", hash)
}

func HammingDistance(a, b uint64) int {
	return bits.OnesCount64(a ^ b)
}

func buildThumbMontage(thumbs []Thumb, cols int) (*image.NRGBA, error) {
	if len(thumbs) == 0 {
		return nil, errors.New("no thumbnails")
	}
	if cols <= 0 {
		cols = int(math.Ceil(math.Sqrt(float64(len(thumbs)))))
	}
	ordered := append([]Thumb(nil), thumbs...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Index < ordered[j].Index })
	cellW, cellH := ordered[0].Width, ordered[0].Height
	if cellW <= 0 || cellH <= 0 {
		return nil, errors.New("invalid thumbnail size")
	}
	rows := int(math.Ceil(float64(len(ordered)) / float64(cols)))
	out := imaging.New(cols*cellW, rows*cellH, color.NRGBA{0, 0, 0, 255})
	for _, th := range ordered {
		if th.Width != cellW || th.Height != cellH {
			return nil, errors.New("thumbnail sizes do not match")
		}
		img, err := thumbImageView(th)
		if err != nil {
			return nil, err
		}
		x := (th.Index % cols) * cellW
		y := (th.Index / cols) * cellH
		draw.Draw(out, image.Rect(x, y, x+cellW, y+cellH), img, image.Point{}, draw.Src)
	}
	return out, nil
}

// PHashImage is a small helper for callers that already have an image.Image.
func PHashImage(img image.Image, opts PHashOptions) uint64 {
	h, _ := computePHashImageHash(img, opts)
	return h
}

// PHashImageWithError is like PHashImage, but it exposes goimagehash/imaging
// errors to callers that want strict error handling.
func PHashImageWithError(img image.Image, opts PHashOptions) (uint64, error) {
	return computePHashImageHash(img, opts)
}

// ThumbImage converts one thumbnail's packed RGBA bytes to an image.RGBA copy.
func ThumbImage(th Thumb) (*image.RGBA, error) {
	img, err := thumbImageView(th)
	if err != nil {
		return nil, err
	}
	copyImg := image.NewRGBA(img.Bounds())
	copy(copyImg.Pix, img.Pix)
	return copyImg, nil
}

func thumbImageView(th Thumb) (*image.RGBA, error) {
	if th.Width <= 0 || th.Height <= 0 || len(th.RGBA) < th.Width*th.Height*4 {
		return nil, errors.New("invalid thumbnail")
	}
	return &image.RGBA{
		Pix:    th.RGBA[:th.Width*th.Height*4],
		Stride: th.Width * 4,
		Rect:   image.Rect(0, 0, th.Width, th.Height),
	}, nil
}

// SolidThumb is useful for tests and callers that want to construct thumbnails
// directly before calling ComputePHashFromThumbnails.
func SolidThumb(index, w, h int, c color.RGBA) Thumb {
	rgba := make([]byte, w*h*4)
	for i := 0; i < len(rgba); i += 4 {
		rgba[i+0] = c.R
		rgba[i+1] = c.G
		rgba[i+2] = c.B
		rgba[i+3] = c.A
	}
	return Thumb{Index: index, Width: w, Height: h, RGBA: rgba}
}

// SavePHashText writes the hex form to disk for workflows that want a sidecar.
func SavePHashText(path string, hash uint64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(FormatPHash(hash)+"\n"), 0644)
}

func readAllSeekStart(r io.ReadSeeker) ([]byte, error) {
	_, _ = r.Seek(0, io.SeekStart)
	b, err := io.ReadAll(r)
	_, _ = r.Seek(0, io.SeekStart)
	return b, err
}

// PHashHexFromGoImageHashString accepts goimagehash strings such as
// "p:0123..." and returns only the hex payload. It is handy for callers moving
// between this package's Stash-style hex and goimagehash serialization.
func PHashHexFromGoImageHashString(s string) string {
	if i := strings.IndexByte(s, ':'); i >= 0 {
		return s[i+1:]
	}
	return s
}
