package previewer

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/disintegration/imaging"
)

type thumbJob struct {
	Index int
	At    float64
	Start float64
	End   float64
}

type thumbResult struct {
	Thumb Thumb
	Err   error
}

type previewJob struct {
	Index int
	Start float64
	Path  string
}

type previewJobResult struct {
	Index int
	Path  string
	Meta  copiedSliceMeta
	Err   error
}

func ProbeSource(ctx context.Context, src Source) (MediaInfo, error) {
	prepared, err := prepareSource(ctx, src)
	if err != nil {
		return MediaInfo{}, err
	}
	if prepared.cleanup != nil {
		defer prepared.cleanup()
	}
	return Probe(prepared.path)
}

func Generate(ctx context.Context, src Source, outputs OutputPaths, opts Options) (Result, error) {
	opts.applyDefaults()
	if outputs.Dir == "" {
		outputs.Dir = "out"
	}
	if outputs.SpritePath == "" {
		outputs.SpritePath = filepath.Join(outputs.Dir, "sprite.jpg")
	}
	if outputs.VTTPath == "" {
		outputs.VTTPath = filepath.Join(outputs.Dir, "sprite.vtt")
	}
	if outputs.PreviewPath == "" {
		outputs.PreviewPath = filepath.Join(outputs.Dir, "preview.mp4")
	}
	if err := os.MkdirAll(outputs.Dir, 0755); err != nil {
		return Result{}, err
	}

	prepared, err := prepareSource(ctx, src)
	if err != nil {
		return Result{}, err
	}
	if prepared.cleanup != nil {
		defer prepared.cleanup()
	}

	info, err := Probe(prepared.path)
	if err != nil {
		return Result{}, err
	}
	res := Result{Info: info}
	// Sprite and preview are independent FFmpeg pipelines. Run both at the same
	// time in the convenience Generate(...) flow so neither long-lived pipeline
	// has to be initialized after the other has already exercised FFmpeg's codec
	// and muxer internals. Dedicated APIs remain fully independent.
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	var sprite *SpriteResult
	var preview *PreviewResult

	if opts.Preview.Enabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := generatePreviewFromPath(workCtx, prepared.path, outputs.PreviewPath, info, opts.Preview, opts.Workers, opts.Progress)
			if err != nil {
				errCh <- err
				cancel()
				return
			}
			preview = p
		}()
	}
	if opts.Sprite.Enabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := generateSpriteFromPath(workCtx, prepared.path, outputs.SpritePath, outputs.VTTPath, info, opts.Sprite, opts.Workers, opts.Progress)
			if err != nil {
				errCh <- err
				cancel()
				return
			}
			sprite = s
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return res, err
		}
	}
	res.Preview = preview
	res.Sprite = sprite
	return res, nil
}

// ExtractThumbnails seeks through src in parallel and returns the raw RGBA
// thumbnails used to build a sprite. The function does not write files.
func ExtractThumbnails(ctx context.Context, src Source, opts SpriteOptions, workers int) ([]Thumb, MediaInfo, error) {
	if workers <= 0 {
		workers = 1
	}
	prepared, err := prepareSource(ctx, src)
	if err != nil {
		return nil, MediaInfo{}, err
	}
	if prepared.cleanup != nil {
		defer prepared.cleanup()
	}
	info, err := Probe(prepared.path)
	if err != nil {
		return nil, MediaInfo{}, err
	}
	thumbs, err := extractThumbnailsFromPath(ctx, prepared.path, info, opts, workers, nil)
	return thumbs, info, err
}

// GenerateSprite creates only sprite.jpg and sprite.vtt from src. It is the
// public API for callers that want sprite/thumb generation without preview MP4.
func GenerateSprite(ctx context.Context, src Source, spritePath, vttPath string, opts SpriteOptions, workers int) (*SpriteResult, MediaInfo, error) {
	if spritePath == "" {
		return nil, MediaInfo{}, errors.New("spritePath is required")
	}
	if vttPath == "" {
		return nil, MediaInfo{}, errors.New("vttPath is required")
	}
	if workers <= 0 {
		workers = 1
	}
	if err := os.MkdirAll(filepath.Dir(spritePath), 0755); err != nil {
		return nil, MediaInfo{}, err
	}
	if err := os.MkdirAll(filepath.Dir(vttPath), 0755); err != nil {
		return nil, MediaInfo{}, err
	}
	prepared, err := prepareSource(ctx, src)
	if err != nil {
		return nil, MediaInfo{}, err
	}
	if prepared.cleanup != nil {
		defer prepared.cleanup()
	}
	info, err := Probe(prepared.path)
	if err != nil {
		return nil, MediaInfo{}, err
	}
	res, err := generateSpriteFromPath(ctx, prepared.path, spritePath, vttPath, info, opts, workers, nil)
	return res, info, err
}

// GeneratePreview creates only preview.mp4 from src. It is the public API for
// callers that want fast preview-video generation without sprite or pHash work.
func GeneratePreview(ctx context.Context, src Source, previewPath string, opts PreviewOptions, workers int) (*PreviewResult, MediaInfo, error) {
	if previewPath == "" {
		return nil, MediaInfo{}, errors.New("previewPath is required")
	}
	if workers <= 0 {
		workers = 1
	}
	if err := os.MkdirAll(filepath.Dir(previewPath), 0755); err != nil {
		return nil, MediaInfo{}, err
	}
	prepared, err := prepareSource(ctx, src)
	if err != nil {
		return nil, MediaInfo{}, err
	}
	if prepared.cleanup != nil {
		defer prepared.cleanup()
	}
	info, err := Probe(prepared.path)
	if err != nil {
		return nil, MediaInfo{}, err
	}
	res, err := generatePreviewFromPath(ctx, prepared.path, previewPath, info, opts, workers, nil)
	return res, info, err
}

func CopyPreviewSlices(ctx context.Context, src Source, outDir string, opts PreviewOptions, workers int) ([]PreviewPart, MediaInfo, error) {
	if outDir == "" {
		return nil, MediaInfo{}, errors.New("outDir is required")
	}
	if workers <= 0 {
		workers = 1
	}
	if opts.Slices <= 0 {
		opts.Slices = 16
	}
	if opts.SliceSeconds <= 0 {
		opts.SliceSeconds = 2.5
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return nil, MediaInfo{}, err
	}
	prepared, err := prepareSource(ctx, src)
	if err != nil {
		return nil, MediaInfo{}, err
	}
	if prepared.cleanup != nil {
		defer prepared.cleanup()
	}
	info, err := Probe(prepared.path)
	if err != nil {
		return nil, MediaInfo{}, err
	}
	parts, err := copyPreviewSlicesFromPath(ctx, prepared.path, outDir, info, opts, workers, nil)
	return parts, info, err
}

func generatePreviewFromPath(ctx context.Context, input, output string, info MediaInfo, opts PreviewOptions, workers int, progress func(ProgressEvent)) (*PreviewResult, error) {
	if workers <= 0 {
		workers = 1
	}
	if opts.Slices <= 0 {
		opts.Slices = 16
	}
	if opts.SliceSeconds <= 0 {
		opts.SliceSeconds = 2.5
	}
	if info.Duration <= 0 {
		return nil, errors.New("input duration is unknown; cannot distribute preview slices")
	}

	baseDir := filepath.Dir(output)
	partsDir := filepath.Join(baseDir, ".preview-parts")
	if err := os.RemoveAll(partsDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(partsDir, 0755); err != nil {
		return nil, err
	}
	if !opts.KeepParts {
		defer os.RemoveAll(partsDir)
	}

	parts, err := copyPreviewSlicesFromPath(ctx, input, partsDir, info, opts, workers, progress)
	if err != nil {
		return nil, err
	}

	resizeFinal := opts.Width > 0 && info.Width > 0 && opts.Width < info.Width
	listPath := filepath.Join(partsDir, "concat.txt")
	if err := writeConcatList(listPath, parts); err != nil {
		return nil, err
	}

	// Final leg only: decode the concat demuxer with inpoint/outpoint and encode
	// once. This preserves the fast parallel stream-copy slice step while avoiding
	// duplicate GOP pre-roll frames in the final preview.
	targetWidth := 0
	if resizeFinal {
		targetWidth = opts.Width
	}
	if err := TranscodeConcatVideo(listPath, output, targetWidth, info.FPS); err != nil {
		return nil, err
	}
	if progress != nil {
		progress(ProgressEvent{Stage: "preview", Done: 1, Total: 1, Path: output})
	}
	return &PreviewResult{PreviewPath: output, Parts: parts}, nil
}

func copyPreviewSlicesFromPath(ctx context.Context, input, partsDir string, info MediaInfo, opts PreviewOptions, workers int, progress func(ProgressEvent)) ([]PreviewPart, error) {
	if info.Duration <= 0 {
		return nil, errors.New("input duration is unknown; cannot distribute preview slices")
	}
	sliceSeconds := opts.SliceSeconds
	if info.Duration < sliceSeconds {
		sliceSeconds = info.Duration
	}

	jobs := make([]previewJob, opts.Slices)
	offset := effectiveEdgeOffset(info.Duration, sliceSeconds, opts.OffsetSeconds)
	minStart := offset
	maxStart := math.Max(minStart, info.Duration-offset-sliceSeconds)
	for i := 0; i < opts.Slices; i++ {
		start := minStart
		if opts.Slices > 1 {
			start += (float64(i) / float64(opts.Slices-1)) * (maxStart - minStart)
		}
		jobs[i] = previewJob{Index: i, Start: start, Path: filepath.Join(partsDir, fmt.Sprintf("part_%04d.mp4", i))}
	}
	if workers > len(jobs) {
		workers = len(jobs)
	}
	if workers <= 0 {
		workers = 1
	}

	jobCh := make(chan previewJob, len(jobs))
	for _, j := range jobs {
		jobCh <- j
	}
	close(jobCh)

	resCh := make(chan previewJobResult, len(jobs))
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := range jobCh {
				if err := checkContext(ctx); err != nil {
					resCh <- previewJobResult{Index: j.Index, Err: err}
					continue
				}
				meta, err := CopyVideoSliceDetailed(input, j.Path, j.Start, sliceSeconds)
				if err != nil {
					resCh <- previewJobResult{Index: j.Index, Err: fmt.Errorf("preview worker %d part %d: %w", id, j.Index, err)}
					continue
				}
				resCh <- previewJobResult{Index: j.Index, Path: j.Path, Meta: meta}
			}
		}(w)
	}

	go func() {
		wg.Wait()
		close(resCh)
	}()

	parts := make([]PreviewPart, opts.Slices)
	got := 0
	var firstErr error
	for r := range resCh {
		if r.Err != nil {
			if firstErr == nil {
				firstErr = r.Err
			}
			continue
		}
		j := jobs[r.Index]
		parts[r.Index] = PreviewPart{
			Index:          r.Index,
			Start:          j.Start,
			Duration:       sliceSeconds,
			Path:           r.Path,
			ActualStart:    r.Meta.ActualStart,
			Inpoint:        r.Meta.Inpoint,
			Outpoint:       r.Meta.Outpoint,
			CopiedDuration: r.Meta.CopiedDuration,
		}
		got++
		if progress != nil {
			progress(ProgressEvent{Stage: "preview-slices", Done: got, Total: opts.Slices, Path: r.Path})
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	if got != opts.Slices {
		return nil, fmt.Errorf("got %d/%d preview slices", got, opts.Slices)
	}
	return parts, nil
}

func writeConcatList(path string, parts []PreviewPart) error {
	var b strings.Builder
	for _, part := range parts {
		// Match ffmpeg concat demuxer usage with -safe 0: write absolute paths.
		// This avoids path resolution surprises when the caller uses a relative
		// output directory while still keeping the list valid for libavformat.
		abs, err := filepath.Abs(part.Path)
		if err != nil {
			return err
		}
		entry := filepath.ToSlash(abs)
		b.WriteString("file '")
		b.WriteString(escapeConcatPath(entry))
		b.WriteString("'\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0644)
}

func escapeConcatPath(path string) string {
	// FFmpeg concat list uses single-quoted strings. Escape a literal single quote
	// with the same shell-like sequence accepted by FFmpeg's parser.
	return strings.ReplaceAll(path, "'", "'\\''")
}

func generateSpriteFromPath(ctx context.Context, input, spritePath, vttPath string, info MediaInfo, opts SpriteOptions, workers int, progress func(ProgressEvent)) (*SpriteResult, error) {
	thumbs, err := extractThumbnailsFromPath(ctx, input, info, opts, workers, progress)
	if err != nil {
		return nil, err
	}
	if err := WriteSpriteJPEG(spritePath, thumbs, opts.Columns, opts.JPEGQuality); err != nil {
		return nil, err
	}
	if err := WriteVTT(vttPath, filepath.Base(spritePath), thumbs, opts.Columns); err != nil {
		return nil, err
	}
	if progress != nil {
		progress(ProgressEvent{Stage: "sprite", Done: 1, Total: 1, Path: spritePath})
	}
	return &SpriteResult{SpritePath: spritePath, VTTPath: vttPath, Thumbs: thumbs}, nil
}

func extractThumbnailsFromPath(ctx context.Context, input string, info MediaInfo, opts SpriteOptions, workers int, progress func(ProgressEvent)) ([]Thumb, error) {
	if opts.Columns <= 0 || opts.Rows <= 0 {
		return nil, errors.New("sprite columns and rows must be positive")
	}
	if opts.ThumbWidth <= 0 {
		opts.ThumbWidth = 160
	}
	if workers <= 0 {
		workers = 1
	}
	total := opts.Columns * opts.Rows
	if info.Duration <= 0 {
		return nil, errors.New("input duration is unknown; cannot distribute sprite timestamps")
	}

	jobs := make([]thumbJob, total)
	interval := info.Duration / float64(total)
	offset := effectiveEdgeOffset(info.Duration, 0.1, opts.OffsetSeconds)
	sampleRange := math.Max(0, info.Duration-2*offset)
	for i := 0; i < total; i++ {
		st := float64(i) * interval
		en := float64(i+1) * interval
		at := offset + (float64(i)+0.5)*sampleRange/float64(total)
		at = math.Max(0.05, math.Min(at, info.Duration-0.05))
		jobs[i] = thumbJob{Index: i, At: at, Start: st, End: en}
	}

	if workers > total {
		workers = total
	}
	if workers <= 0 {
		workers = 1
	}

	jobCh := make(chan thumbJob, total)
	for _, j := range jobs {
		jobCh <- j
	}
	close(jobCh)

	resCh := make(chan thumbResult, total)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			dec, err := openDecoder(input)
			if err != nil {
				resCh <- thumbResult{Err: fmt.Errorf("worker %d: %w", id, err)}
				return
			}
			defer dec.Close()
			for j := range jobCh {
				if err := checkContext(ctx); err != nil {
					resCh <- thumbResult{Err: err}
					continue
				}
				th, err := dec.SeekThumbnail(j.At, opts.ThumbWidth)
				if err != nil {
					resCh <- thumbResult{Err: fmt.Errorf("worker %d index %d: %w", id, j.Index, err)}
					continue
				}
				th.Index = j.Index
				th.Start = j.Start
				th.End = j.End
				resCh <- thumbResult{Thumb: th}
			}
		}(w)
	}

	go func() {
		wg.Wait()
		close(resCh)
	}()

	thumbs := make([]Thumb, total)
	got := 0
	var firstErr error
	for r := range resCh {
		if r.Err != nil {
			if firstErr == nil {
				firstErr = r.Err
			}
			continue
		}
		thumbs[r.Thumb.Index] = r.Thumb
		got++
		if progress != nil {
			progress(ProgressEvent{Stage: "sprite-thumbnails", Done: got, Total: total})
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	if got != total {
		return nil, fmt.Errorf("got %d/%d thumbnails", got, total)
	}

	sort.Slice(thumbs, func(i, j int) bool { return thumbs[i].Index < thumbs[j].Index })
	return thumbs, nil
}

func effectiveEdgeOffset(duration, requiredSpan, requested float64) float64 {
	if requested <= 0 || duration <= requiredSpan {
		return 0
	}
	return math.Min(requested, (duration-requiredSpan)/2)
}

func WriteSpriteJPEG(path string, thumbs []Thumb, cols int, quality int) error {
	if len(thumbs) == 0 {
		return errors.New("no thumbnails")
	}
	if cols <= 0 {
		return errors.New("sprite columns must be positive")
	}
	cellW, cellH := thumbs[0].Width, thumbs[0].Height
	if cellW <= 0 || cellH <= 0 {
		return errors.New("invalid thumbnail size")
	}
	rows := int(math.Ceil(float64(len(thumbs)) / float64(cols)))
	sprite := image.NewRGBA(image.Rect(0, 0, cols*cellW, rows*cellH))

	for _, t := range thumbs {
		if t.Width != cellW || t.Height != cellH {
			return errors.New("thumbnail sizes do not match")
		}
		if len(t.RGBA) < t.Width*t.Height*4 {
			return errors.New("thumbnail RGBA buffer too small")
		}
		img := &image.RGBA{
			Pix:    t.RGBA[:t.Width*t.Height*4],
			Stride: t.Width * 4,
			Rect:   image.Rect(0, 0, t.Width, t.Height),
		}
		x := (t.Index % cols) * cellW
		y := (t.Index / cols) * cellH
		draw.Draw(sprite, image.Rect(x, y, x+t.Width, y+t.Height), img, image.Point{}, draw.Src)
	}

	if quality <= 0 || quality > 100 {
		quality = 82
	}
	return imaging.Save(sprite, path, imaging.JPEGQuality(quality))
}

func WriteVTT(path, spriteName string, thumbs []Thumb, cols int) error {
	if len(thumbs) == 0 {
		return errors.New("no thumbnails")
	}
	cellW, cellH := thumbs[0].Width, thumbs[0].Height
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for _, t := range thumbs {
		x := (t.Index % cols) * cellW
		y := (t.Index / cols) * cellH
		b.WriteString(FormatVTTTime(t.Start))
		b.WriteString(" --> ")
		b.WriteString(FormatVTTTime(t.End))
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("%s#xywh=%d,%d,%d,%d\n\n", spriteName, x, y, cellW, cellH))
	}
	return os.WriteFile(path, []byte(b.String()), 0644)
}

func FormatVTTTime(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	msTotal := int64(math.Round(sec * 1000))
	h := msTotal / 3600000
	msTotal %= 3600000
	m := msTotal / 60000
	msTotal %= 60000
	s := msTotal / 1000
	ms := msTotal % 1000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, ms)
}
