package previewer

import (
	"context"
	"io"
)

// Source describes media input for the library.
//
// Path is the fastest path and lets FFmpeg open the file directly.
// ReadSeeker lets callers pass Go-backed data. For deterministic FFmpeg
// behavior, ReadSeeker inputs are materialized to a temporary file and then
// processed by the same path backend as Path inputs. This makes slices generated
// from Path and ReadSeeker byte-identical when the underlying bytes are equal.
type Source struct {
	Path       string
	ReadSeeker io.ReadSeeker
	Name       string
}

func FromFile(path string) Source {
	return Source{Path: path, Name: path}
}

func FromReadSeeker(name string, r io.ReadSeeker) Source {
	return Source{Name: name, ReadSeeker: r}
}

type Options struct {
	Workers  int
	Sprite   SpriteOptions
	Preview  PreviewOptions
	Progress func(ProgressEvent)
}

type SpriteOptions struct {
	Enabled     bool
	Columns     int
	Rows        int
	ThumbWidth  int
	JPEGQuality int
	// FastSeek uses the first decodable frame at or before each requested
	// timestamp instead of decoding forward to the exact timestamp.
	FastSeek bool
	// OffsetSeconds excludes this duration from each edge when selecting frames.
	// VTT cues still span the complete media duration.
	OffsetSeconds float64
}

type PreviewOptions struct {
	Enabled      bool
	Slices       int
	SliceSeconds float64
	Width        int
	// FPS controls preview output frame rate. Zero preserves source FPS.
	FPS       float64
	KeepParts bool
	// OffsetSeconds excludes this duration from each edge when distributing slices.
	OffsetSeconds float64
}

// PHashOptions controls perceptual hash generation. The standard 64-bit path
// samples frames across the middle 90% of the video, combines them into a
// montage, and passes it directly to the DCT pHash implementation.
type PHashOptions struct {
	Columns    int
	Rows       int
	ThumbWidth int
	// ResizeSize applies only to non-standard extended hashes. The standard
	// 64-bit hash ignores it because goimagehash performs its own 64x64 resize.
	ResizeSize int
	HashSize   int
}

type OutputPaths struct {
	Dir         string
	SpritePath  string
	VTTPath     string
	PreviewPath string
}

type Result struct {
	Info    MediaInfo
	Sprite  *SpriteResult
	Preview *PreviewResult
}

type SpriteResult struct {
	SpritePath string
	VTTPath    string
	Thumbs     []Thumb
}

type PreviewResult struct {
	PreviewPath string
	Parts       []PreviewPart
}

type PHashResult struct {
	Hash   uint64
	Hex    string
	Thumbs []Thumb
}

type PreviewPart struct {
	Index          int
	Start          float64
	Duration       float64
	Path           string
	ActualStart    float64
	Inpoint        float64
	Outpoint       float64
	CopiedDuration float64
}

type ProgressEvent struct {
	Stage string
	Done  int
	Total int
	Path  string
	Err   error
}

func DefaultOptions() Options {
	return Options{
		Workers: 4,
		Sprite: SpriteOptions{
			Enabled:     true,
			Columns:     8,
			Rows:        5,
			ThumbWidth:  160,
			JPEGQuality: 82,
		},
		Preview: PreviewOptions{
			Enabled:      true,
			Slices:       16,
			SliceSeconds: 2.5,
			Width:        640,
		},
	}
}

func (o *Options) applyDefaults() {
	if o.Workers <= 0 {
		o.Workers = 1
	}
	if o.Sprite.Columns <= 0 {
		o.Sprite.Columns = 8
	}
	if o.Sprite.Rows <= 0 {
		o.Sprite.Rows = 5
	}
	if o.Sprite.ThumbWidth <= 0 {
		o.Sprite.ThumbWidth = 160
	}
	if o.Sprite.JPEGQuality <= 0 || o.Sprite.JPEGQuality > 100 {
		o.Sprite.JPEGQuality = 82
	}
	if o.Preview.Slices <= 0 {
		o.Preview.Slices = 16
	}
	if o.Preview.SliceSeconds <= 0 {
		o.Preview.SliceSeconds = 2.5
	}
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
