package previewer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testInputPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join("testdata", "sample.mp4")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("test input not available: %s", path)
	}
	return path
}

func externalInputPath(t *testing.T) string {
	t.Helper()
	path := os.Getenv("PREVIEWER_TEST_INPUT")
	if path == "" {
		t.Skip("PREVIEWER_TEST_INPUT not set")
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("external test input not available: %s", path)
	}
	return path
}

func fileSHA256(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

func thumbSHA256(t *testing.T, th Thumb) [32]byte {
	t.Helper()
	return sha256.Sum256(th.RGBA)
}

func TestExternalInputSmoke(t *testing.T) {
	input := externalInputPath(t)
	out := t.TempDir()
	opts := DefaultOptions()
	opts.Workers = 2
	opts.Sprite = SpriteOptions{Enabled: true, Columns: 2, Rows: 2, ThumbWidth: 96, JPEGQuality: 80}
	opts.Preview = PreviewOptions{Enabled: true, Slices: 3, SliceSeconds: 0.75, Width: 320}
	res, err := GenerateFromFile(context.Background(), input, OutputPaths{Dir: out}, opts)
	if err != nil {
		t.Fatalf("external generate: %v", err)
	}
	if res.Sprite == nil || res.Preview == nil {
		t.Fatalf("missing external outputs: %+v", res)
	}
	if _, err := os.Stat(res.Sprite.SpritePath); err != nil {
		t.Fatalf("external sprite missing: %v", err)
	}
	if _, err := os.Stat(res.Preview.PreviewPath); err != nil {
		t.Fatalf("external preview missing: %v", err)
	}
	if phash, _, err := CalculatePHashFromFile(context.Background(), input, PHashOptions{Columns: 2, Rows: 2, ThumbWidth: 96, ResizeSize: 32, HashSize: 8}, 2); err != nil {
		t.Fatalf("external phash: %v", err)
	} else if phash.Hex == "" {
		t.Fatal("external phash empty")
	}
}

func TestAAAPHashFromFileAndReadSeekerMatchExactly(t *testing.T) {
	input := testInputPath(t)
	data, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}

	opts := PHashOptions{Columns: 3, Rows: 2, ThumbWidth: 96, ResizeSize: 32, HashSize: 8}
	fileHash, fileInfo, err := CalculatePHashFromFile(context.Background(), input, opts, 1)
	if err != nil {
		t.Fatalf("file phash: %v", err)
	}
	readerHash, readerInfo, err := CalculatePHashFromReadSeeker(context.Background(), filepath.Base(input), bytes.NewReader(data), opts, 1)
	if err != nil {
		t.Fatalf("reader phash: %v", err)
	}
	if fileInfo != readerInfo {
		t.Fatalf("probe mismatch: file=%+v reader=%+v", fileInfo, readerInfo)
	}
	if fileHash.Hash != readerHash.Hash || fileHash.Hex != readerHash.Hex {
		t.Fatalf("phash mismatch: file=%s reader=%s", fileHash.Hex, readerHash.Hex)
	}
	if len(fileHash.Thumbs) != len(readerHash.Thumbs) {
		t.Fatalf("phash thumb count mismatch: %d != %d", len(fileHash.Thumbs), len(readerHash.Thumbs))
	}
	for i := range fileHash.Thumbs {
		want := thumbSHA256(t, fileHash.Thumbs[i])
		got := thumbSHA256(t, readerHash.Thumbs[i])
		if got != want {
			t.Fatalf("phash thumb %d mismatch: file=%x reader=%x", i, want, got)
		}
	}
}

func TestAABComputePHashFromExtractedThumbnailsMatchesCalculatePHash(t *testing.T) {
	input := testInputPath(t)
	opts := PHashOptions{Columns: 2, Rows: 2, ThumbWidth: 96, ResizeSize: 32, HashSize: 8}
	ph, _, err := CalculatePHashFromFile(context.Background(), input, opts, 1)
	if err != nil {
		t.Fatalf("calculate phash: %v", err)
	}
	thumbs, _, err := ExtractThumbnailsFromFile(context.Background(), input, SpriteOptions{Enabled: true, Columns: opts.Columns, Rows: opts.Rows, ThumbWidth: opts.ThumbWidth}, 1)
	if err != nil {
		t.Fatalf("extract thumbs: %v", err)
	}
	got, err := ComputePHashFromThumbnails(thumbs, opts)
	if err != nil {
		t.Fatalf("compute phash from thumbs: %v", err)
	}
	if got != ph.Hash {
		t.Fatalf("phash mismatch: calculate=%s from-thumbs=%s", ph.Hex, FormatPHash(got))
	}
}

func TestPreviewSlicesFromFileAndReadSeekerMatchExactly(t *testing.T) {
	input := testInputPath(t)
	data, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}

	opts := PreviewOptions{Slices: 4, SliceSeconds: 0.75, KeepParts: true}
	fileDir := filepath.Join(t.TempDir(), "file")
	readerDir := filepath.Join(t.TempDir(), "reader")

	fileParts, fileInfo, err := CopyPreviewSlices(context.Background(), FromFile(input), fileDir, opts, 2)
	if err != nil {
		t.Fatalf("file slices: %v", err)
	}
	readerParts, readerInfo, err := CopyPreviewSlices(context.Background(), FromReadSeeker(filepath.Base(input), bytes.NewReader(data)), readerDir, opts, 2)
	if err != nil {
		t.Fatalf("reader slices: %v", err)
	}

	if fileInfo != readerInfo {
		t.Fatalf("probe mismatch: file=%+v reader=%+v", fileInfo, readerInfo)
	}
	if len(fileParts) != len(readerParts) {
		t.Fatalf("slice count mismatch: %d != %d", len(fileParts), len(readerParts))
	}
	for i := range fileParts {
		got := fileSHA256(t, readerParts[i].Path)
		want := fileSHA256(t, fileParts[i].Path)
		if got != want {
			t.Fatalf("slice %d mismatch: file=%x reader=%x", i, want, got)
		}
	}
}

func TestPreviewSlicesRespectEdgeOffset(t *testing.T) {
	input := filepath.Join("testdata", "sample.mp4")
	parts, info, err := CopyPreviewSlices(context.Background(), FromFile(input), t.TempDir(), PreviewOptions{
		Slices: 3, SliceSeconds: 0.5, OffsetSeconds: 1,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if parts[0].Start < 1 {
		t.Fatalf("first preview start = %f, want >= 1", parts[0].Start)
	}
	if last := parts[len(parts)-1]; last.Start+last.Duration > info.Duration-1+0.001 {
		t.Fatalf("last preview ends at %f, want <= %f", last.Start+last.Duration, info.Duration-1)
	}
}

func TestThumbnailOffsetPreservesTimelineCues(t *testing.T) {
	input := filepath.Join("testdata", "sample.mp4")
	thumbs, info, err := ExtractThumbnailsFromFile(context.Background(), input, SpriteOptions{
		Columns: 2, Rows: 2, ThumbWidth: 96, OffsetSeconds: 1,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if thumbs[0].Start != 0 {
		t.Fatalf("first cue starts at %f, want 0", thumbs[0].Start)
	}
	if got := thumbs[len(thumbs)-1].End; math.Abs(got-info.Duration) > 0.001 {
		t.Fatalf("last cue ends at %f, want %f", got, info.Duration)
	}
}

func TestGeneratePreviewWithRelativeOutputDirDoesNotDuplicatePartsPath(t *testing.T) {
	input, err := filepath.Abs(testInputPath(t))
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	info, err := ProbeFile(context.Background(), input)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	opts := PreviewOptions{Enabled: true, Slices: 2, SliceSeconds: 0.5, Width: 0, KeepParts: true}
	res, err := generatePreviewFromPath(context.Background(), input, filepath.Join("test", "preview.mp4"), info, opts, 1, nil)
	if err != nil {
		t.Fatalf("generate preview with relative output dir: %v", err)
	}
	if _, err := os.Stat(res.PreviewPath); err != nil {
		t.Fatalf("preview missing: %v", err)
	}
	listBytes, err := os.ReadFile(filepath.Join("test", ".preview-parts", "concat.txt"))
	if err != nil {
		t.Fatalf("concat list missing: %v", err)
	}
	if !bytes.Contains(listBytes, []byte("-parts/part_0000.mp4")) && !bytes.Contains(listBytes, []byte(".preview-parts/part_0000.mp4")) {
		t.Fatalf("concat list should contain absolute part paths for safe=0, got:\n%s", listBytes)
	}
}

func TestExternalPreviewPartsAreDistinctAndConcatListIsSafe(t *testing.T) {
	input := externalInputPath(t)
	out := t.TempDir()
	info, err := ProbeFile(context.Background(), input)
	if err != nil {
		t.Fatalf("probe external input: %v", err)
	}
	opts := PreviewOptions{Enabled: true, Slices: 6, SliceSeconds: 1.0, Width: 320, KeepParts: true}
	res, err := generatePreviewFromPath(context.Background(), input, filepath.Join(out, "preview.mp4"), info, opts, 3, nil)
	if err != nil {
		t.Fatalf("generate external preview: %v", err)
	}
	if _, err := os.Stat(res.PreviewPath); err != nil {
		t.Fatalf("preview missing: %v", err)
	}
	seen := map[[32]byte]string{}
	for _, part := range res.Parts {
		h := fileSHA256(t, part.Path)
		if prev, ok := seen[h]; ok {
			t.Fatalf("preview part %s is byte-identical to %s; expected distinct seek windows", part.Path, prev)
		}
		seen[h] = part.Path
		if part.CopiedDuration <= 0 || part.CopiedDuration > opts.SliceSeconds+0.25 {
			t.Fatalf("part duration should stay close to requested slice duration: %+v", part)
		}
		if part.Inpoint != 0 || part.Outpoint != 0 {
			t.Fatalf("copy slices should not use concat trim metadata anymore: %+v", part)
		}
	}
	listBytes, err := os.ReadFile(filepath.Join(filepath.Dir(res.Parts[0].Path), "concat.txt"))
	if err != nil {
		t.Fatalf("concat list missing: %v", err)
	}
	if bytes.HasPrefix(listBytes, []byte("ffconcat version 1.0\n")) {
		t.Fatalf("concat list should match basic ffmpeg concat list format, got:\n%s", listBytes)
	}
	if !bytes.Contains(listBytes, []byte(out)) || !bytes.Contains(listBytes, []byte(".preview-parts/")) {
		t.Fatalf("concat list should use absolute paths for safe=0, got:\n%s", listBytes)
	}
	if bytes.Contains(listBytes, []byte("inpoint ")) || bytes.Contains(listBytes, []byte("outpoint ")) {
		t.Fatalf("concat list should not include inpoint/outpoint trim directives, got:\n%s", listBytes)
	}
}

func TestThumbnailsFromFileAndReadSeekerMatchExactly(t *testing.T) {
	input := testInputPath(t)
	data, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}

	opts := SpriteOptions{Enabled: true, Columns: 3, Rows: 2, ThumbWidth: 96, JPEGQuality: 80}
	fileThumbs, fileInfo, err := ExtractThumbnailsFromFile(context.Background(), input, opts, 1)
	if err != nil {
		t.Fatalf("file thumbs: %v", err)
	}
	readerThumbs, readerInfo, err := ExtractThumbnailsFromReadSeeker(context.Background(), filepath.Base(input), bytes.NewReader(data), opts, 1)
	if err != nil {
		t.Fatalf("reader thumbs: %v", err)
	}

	if fileInfo != readerInfo {
		t.Fatalf("probe mismatch: file=%+v reader=%+v", fileInfo, readerInfo)
	}
	if len(fileThumbs) != len(readerThumbs) {
		t.Fatalf("thumb count mismatch: %d != %d", len(fileThumbs), len(readerThumbs))
	}
	for i := range fileThumbs {
		f := fileThumbs[i]
		r := readerThumbs[i]
		if f.Index != r.Index || f.Start != r.Start || f.End != r.End || f.Width != r.Width || f.Height != r.Height {
			t.Fatalf("thumb %d metadata mismatch: file=%+v reader=%+v", i, f, r)
		}
		want := thumbSHA256(t, f)
		got := thumbSHA256(t, r)
		if got != want {
			t.Fatalf("thumb %d RGBA mismatch: file=%x reader=%x", i, want, got)
		}
	}
}

func TestGenerateSpriteFromFileAndReadSeekerMatchExactly(t *testing.T) {
	input := testInputPath(t)
	data, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}

	opts := SpriteOptions{Enabled: true, Columns: 2, Rows: 2, ThumbWidth: 96, JPEGQuality: 80}
	fileOut := t.TempDir()
	readerOut := t.TempDir()

	fileRes, fileInfo, err := GenerateSpriteFromFile(
		context.Background(),
		input,
		filepath.Join(fileOut, "sprite.jpg"),
		filepath.Join(fileOut, "sprite.vtt"),
		opts,
		1,
	)
	if err != nil {
		t.Fatalf("file sprite: %v", err)
	}
	readerRes, readerInfo, err := GenerateSpriteFromReadSeeker(
		context.Background(),
		filepath.Base(input),
		bytes.NewReader(data),
		filepath.Join(readerOut, "sprite.jpg"),
		filepath.Join(readerOut, "sprite.vtt"),
		opts,
		1,
	)
	if err != nil {
		t.Fatalf("reader sprite: %v", err)
	}
	if fileInfo != readerInfo {
		t.Fatalf("probe mismatch: file=%+v reader=%+v", fileInfo, readerInfo)
	}

	checks := []struct{ name, filePath, readerPath string }{
		{"sprite", fileRes.SpritePath, readerRes.SpritePath},
		{"vtt", fileRes.VTTPath, readerRes.VTTPath},
	}
	for _, c := range checks {
		want := fileSHA256(t, c.filePath)
		got := fileSHA256(t, c.readerPath)
		if got != want {
			t.Fatalf("%s mismatch: file=%x reader=%x", c.name, want, got)
		}
	}
}

func TestGenerateFromFileAndReadSeekerMatchExactlyForOutputs(t *testing.T) {
	input := testInputPath(t)
	data, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}

	opts := DefaultOptions()
	opts.Workers = 1
	opts.Sprite = SpriteOptions{Enabled: true, Columns: 2, Rows: 2, ThumbWidth: 96, JPEGQuality: 80}
	opts.Preview = PreviewOptions{Enabled: true, Slices: 3, SliceSeconds: 0.75, Width: 0, KeepParts: true}

	fileOut := filepath.Join(t.TempDir(), "file")
	readerOut := filepath.Join(t.TempDir(), "reader")

	fileRes, err := GenerateFromFile(context.Background(), input, OutputPaths{Dir: fileOut}, opts)
	if err != nil {
		t.Fatalf("file generate: %v", err)
	}
	readerRes, err := GenerateFromReadSeeker(context.Background(), filepath.Base(input), bytes.NewReader(data), OutputPaths{Dir: readerOut}, opts)
	if err != nil {
		t.Fatalf("reader generate: %v", err)
	}

	checks := []struct{ name, filePath, readerPath string }{
		{"sprite", fileRes.Sprite.SpritePath, readerRes.Sprite.SpritePath},
		{"vtt", fileRes.Sprite.VTTPath, readerRes.Sprite.VTTPath},
		{"preview", fileRes.Preview.PreviewPath, readerRes.Preview.PreviewPath},
	}
	for _, c := range checks {
		want := fileSHA256(t, c.filePath)
		got := fileSHA256(t, c.readerPath)
		if got != want {
			t.Fatalf("%s mismatch: file=%x reader=%x", c.name, want, got)
		}
	}
	for i := range fileRes.Preview.Parts {
		name := fmt.Sprintf("part %d", i)
		want := fileSHA256(t, fileRes.Preview.Parts[i].Path)
		got := fileSHA256(t, readerRes.Preview.Parts[i].Path)
		if got != want {
			t.Fatalf("%s mismatch: file=%x reader=%x", name, want, got)
		}
	}
}

func TestPreviewFromPublicAPIMatchesFFmpegCLIReference(t *testing.T) {
	ffmpegPath := ffmpegCLIPath(t)
	input := testInputPath(t)
	absInput, err := filepath.Abs(input)
	if err != nil {
		t.Fatal(err)
	}
	info, err := ProbeFile(context.Background(), absInput)
	if err != nil {
		t.Fatalf("probe input: %v", err)
	}

	opts := PreviewOptions{Enabled: true, Slices: 4, SliceSeconds: 0.75, Width: 320, KeepParts: true}
	t.Log("start api generate")
	t.Log("start external api generate")
	apiOut := filepath.Join(t.TempDir(), "api")
	apiRes, _, err := GeneratePreviewFromFile(context.Background(), absInput, filepath.Join(apiOut, "preview.mp4"), opts, 2)
	if err != nil {
		t.Fatalf("api preview generation failed: %v", err)
	}
	if apiRes == nil {
		t.Fatal("api preview result missing")
	}

	t.Log("start ffmpeg reference")
	t.Log("start external ffmpeg reference")
	refOut := filepath.Join(t.TempDir(), "ffmpeg-reference")
	refPreview := generateFFmpegCLIReferencePreview(t, ffmpegPath, absInput, refOut, info, opts)

	apiHash, apiLen := decodeRawYUV420PHash(t, ffmpegPath, apiRes.PreviewPath)
	refHash, refLen := decodeRawYUV420PHash(t, ffmpegPath, refPreview)
	if apiHash != refHash || apiLen != refLen {
		t.Fatalf("public API preview does not match ffmpeg CLI decoded frames: api=%x len=%d ref=%x len=%d", apiHash, apiLen, refHash, refLen)
	}
}

func TestExternalPreviewFromPublicAPIMatchesFFmpegCLIReference(t *testing.T) {
	if os.Getenv("PREVIEWER_SLOW_FFMPEG_PARITY") != "1" {
		t.Skip("set PREVIEWER_SLOW_FFMPEG_PARITY=1 to run the large external API-vs-FFmpeg parity regression")
	}
	ffmpegPath := ffmpegCLIPath(t)
	input := externalInputPath(t)
	absInput, err := filepath.Abs(input)
	if err != nil {
		t.Fatal(err)
	}
	info, err := ProbeFile(context.Background(), absInput)
	if err != nil {
		t.Fatalf("probe input: %v", err)
	}

	opts := PreviewOptions{Enabled: true, Slices: 12, SliceSeconds: 2.5, Width: 640, KeepParts: true}
	apiOut := filepath.Join(t.TempDir(), "api")
	apiRes, _, err := GeneratePreviewFromFile(context.Background(), absInput, filepath.Join(apiOut, "preview.mp4"), opts, 4)
	if err != nil {
		t.Fatalf("api preview generation failed: %v", err)
	}
	if apiRes == nil {
		t.Fatal("api preview result missing")
	}

	refOut := filepath.Join(t.TempDir(), "ffmpeg-reference")
	refPreview := generateFFmpegCLIReferencePreview(t, ffmpegPath, absInput, refOut, info, opts)

	apiHash, apiLen := decodeRawYUV420PHash(t, ffmpegPath, apiRes.PreviewPath)
	refHash, refLen := decodeRawYUV420PHash(t, ffmpegPath, refPreview)
	if apiHash != refHash || apiLen != refLen {
		t.Fatalf("external public API preview does not match ffmpeg CLI decoded frames: api=%x len=%d ref=%x len=%d", apiHash, apiLen, refHash, refLen)
	}
}

func ffmpegCLIPath(tb testing.TB) string {
	tb.Helper()
	if path := os.Getenv("PREVIEWER_FFMPEG_CLI"); path != "" {
		if _, err := os.Stat(path); err != nil {
			tb.Fatalf("PREVIEWER_FFMPEG_CLI points to unavailable ffmpeg binary %q: %v", path, err)
		}
		return path
	}
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		tb.Skip("ffmpeg CLI not available for reference regression/benchmark")
	}
	return path
}

func ffmpegCLICommand(ffmpegPath string, args ...string) *exec.Cmd {
	cmd := exec.Command(ffmpegPath, args...)
	if dir := filepath.Dir(ffmpegPath); dir != "." && dir != "" {
		cmd.Dir = dir
	}
	return cmd
}

func generateFFmpegCLIReferencePreview(tb testing.TB, ffmpegPath, input, outDir string, info MediaInfo, opts PreviewOptions) string {
	tb.Helper()
	partsDir := filepath.Join(outDir, "parts")
	if err := os.MkdirAll(partsDir, 0755); err != nil {
		tb.Fatal(err)
	}

	sliceSeconds := opts.SliceSeconds
	if info.Duration < sliceSeconds {
		sliceSeconds = info.Duration
	}
	maxStart := math.Max(0, info.Duration-sliceSeconds)
	parts := make([]string, opts.Slices)
	for i := 0; i < opts.Slices; i++ {
		start := 0.0
		if opts.Slices > 1 {
			start = (float64(i) / float64(opts.Slices-1)) * maxStart
		}
		part := filepath.Join(partsDir, fmt.Sprintf("part_%04d.mp4", i))
		parts[i] = part
		cmd := ffmpegCLICommand(ffmpegPath,
			"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
			"-ss", fmt.Sprintf("%.9f", start),
			"-i", input,
			"-t", fmt.Sprintf("%.9f", sliceSeconds),
			"-an", "-c:v", "copy",
			part,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			tb.Fatalf("ffmpeg reference slice %d failed: %v\n%s", i, err, out)
		}
	}

	listPath := filepath.Join(outDir, "concat.txt")
	var list strings.Builder
	for _, part := range parts {
		abs, err := filepath.Abs(part)
		if err != nil {
			tb.Fatal(err)
		}
		list.WriteString("file '")
		list.WriteString(filepath.ToSlash(abs))
		list.WriteString("'\n")
	}
	if err := os.WriteFile(listPath, []byte(list.String()), 0644); err != nil {
		tb.Fatal(err)
	}

	filter := fmt.Sprintf("scale=%d:-2:flags=fast_bilinear,fps=%.12g", opts.Width, info.FPS)
	previewPath := filepath.Join(outDir, "preview.mp4")
	cmd := ffmpegCLICommand(ffmpegPath,
		"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-f", "concat", "-safe", "0", "-i", listPath,
		"-vf", filter,
		"-an", "-c:v", "libx264", "-preset", "ultrafast", "-crf", "28",
		"-pix_fmt", "yuv420p", "-bf", "0", "-g", "48",
		"-movflags", "+faststart",
		previewPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		tb.Fatalf("ffmpeg reference concat failed: %v\n%s", err, out)
	}
	return previewPath
}

func decodeRawYUV420PHash(tb testing.TB, ffmpegPath, input string) ([32]byte, int64) {
	tb.Helper()
	cmd := ffmpegCLICommand(ffmpegPath,
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-i", input,
		"-an", "-f", "rawvideo", "-pix_fmt", "yuv420p", "-",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		tb.Fatalf("decode raw video stdout pipe: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		tb.Fatalf("decode raw video start failed: %v", err)
	}
	h := sha256.New()
	n, copyErr := io.Copy(h, stdout)
	waitErr := cmd.Wait()
	if copyErr != nil {
		tb.Fatalf("decode raw video read failed: %v\n%s", copyErr, stderr.String())
	}
	if waitErr != nil {
		tb.Fatalf("decode raw video failed: %v\n%s", waitErr, stderr.String())
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, n
}

func TestWorkerPoolDoesNotLeakGoroutinesOnInvalidInput(t *testing.T) {
	before := runtime.NumGoroutine()
	_, _, err := ExtractThumbnailsFromFile(context.Background(), filepath.Join(t.TempDir(), "missing.mp4"), SpriteOptions{Columns: 4, Rows: 4, ThumbWidth: 64}, 16)
	if err == nil {
		t.Fatal("expected error for missing input")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		if runtime.NumGoroutine() <= before+2 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("possible goroutine leak: before=%d after=%d", before, runtime.NumGoroutine())
}

func TestRepeatedThumbnailExtractionRSSBounded(t *testing.T) {
	if os.Getenv("PREVIEWER_STRESS_TEST") != "1" {
		t.Skip("set PREVIEWER_STRESS_TEST=1 to run RSS stress check")
	}
	if runtime.GOOS != "linux" {
		t.Skip("RSS check uses /proc")
	}
	input := testInputPath(t)
	opts := SpriteOptions{Columns: 2, Rows: 2, ThumbWidth: 96}
	// Warm up FFmpeg's global/lazy caches so the measured delta is not first-use setup.
	if _, _, err := ExtractThumbnailsFromFile(context.Background(), input, opts, 2); err != nil {
		t.Fatalf("warmup thumbnails: %v", err)
	}
	runtime.GC()
	before := currentRSSBytes(t)
	for i := 0; i < 8; i++ {
		if _, _, err := ExtractThumbnailsFromFile(context.Background(), input, opts, 2); err != nil {
			t.Fatalf("iteration %d thumbnails: %v", i, err)
		}
	}
	runtime.GC()
	after := currentRSSBytes(t)
	const maxGrowth = 128 << 20
	if after > before+maxGrowth {
		t.Fatalf("RSS grew too much: before=%d after=%d delta=%d", before, after, after-before)
	}
}

func currentRSSBytes(t *testing.T) uint64 {
	t.Helper()
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		t.Fatal(err)
	}
	var sizePages, rssPages uint64
	if _, err := fmt.Fscan(bytes.NewReader(b), &sizePages, &rssPages); err != nil {
		t.Fatal(err)
	}
	return rssPages * uint64(os.Getpagesize())
}
