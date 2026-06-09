package previewer

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

func benchmarkInputPath(b *testing.B) string {
	b.Helper()
	for _, key := range []string{"PREVIEWER_BENCH_INPUT", "PREVIEWER_TEST_INPUT"} {
		if path := os.Getenv(key); path != "" {
			if _, err := os.Stat(path); err != nil {
				b.Fatalf("%s points to unavailable input %q: %v", key, path, err)
			}
			abs, err := filepath.Abs(path)
			if err != nil {
				b.Fatal(err)
			}
			return abs
		}
	}
	path := filepath.Join("testdata", "sample.mp4")
	if _, err := os.Stat(path); err != nil {
		b.Skipf("benchmark input not available: %s", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		b.Fatal(err)
	}
	return abs
}

func benchmarkPreviewOptions() PreviewOptions {
	return PreviewOptions{
		Enabled:      true,
		Slices:       envInt("PREVIEWER_BENCH_SLICES", 12),
		SliceSeconds: envFloat("PREVIEWER_BENCH_SLICE_SECONDS", 2.5),
		Width:        envInt("PREVIEWER_BENCH_WIDTH", 640),
		KeepParts:    false,
	}
}

func benchmarkWorkers() int {
	return envInt("PREVIEWER_BENCH_WORKERS", minInt(maxInt(runtime.NumCPU()/2, 1), 4))
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envFloat(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return n
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func BenchmarkPreviewPublicAPI(b *testing.B) {
	input := benchmarkInputPath(b)
	opts := benchmarkPreviewOptions()
	workers := benchmarkWorkers()
	base := b.TempDir()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := filepath.Join(base, "public-api", strconv.Itoa(i), "preview.mp4")
		res, _, err := GeneratePreviewFromFile(context.Background(), input, out, opts, workers)
		if err != nil {
			b.Fatalf("public API preview: %v", err)
		}
		if res == nil || res.PreviewPath == "" {
			b.Fatal("public API preview result missing")
		}
	}
}

func BenchmarkPreviewLibraryCore(b *testing.B) {
	input := benchmarkInputPath(b)
	opts := benchmarkPreviewOptions()
	workers := benchmarkWorkers()
	info, err := ProbeFile(context.Background(), input)
	if err != nil {
		b.Fatalf("probe benchmark input: %v", err)
	}
	base := b.TempDir()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := filepath.Join(base, "library-core", strconv.Itoa(i), "preview.mp4")
		res, err := generatePreviewFromPath(context.Background(), input, out, info, opts, workers, nil)
		if err != nil {
			b.Fatalf("library core preview: %v", err)
		}
		if res == nil || res.PreviewPath == "" {
			b.Fatal("library core preview result missing")
		}
	}
}

func BenchmarkPreviewFFmpegCLIReference(b *testing.B) {
	ffmpegPath := ffmpegCLIPath(b)
	input := benchmarkInputPath(b)
	opts := benchmarkPreviewOptions()
	info, err := ProbeFile(context.Background(), input)
	if err != nil {
		b.Fatalf("probe benchmark input: %v", err)
	}
	base := b.TempDir()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		outDir := filepath.Join(base, "ffmpeg-cli", strconv.Itoa(i))
		previewPath := generateFFmpegCLIReferencePreview(b, ffmpegPath, input, outDir, info, opts)
		if _, err := os.Stat(previewPath); err != nil {
			b.Fatalf("ffmpeg CLI preview missing: %v", err)
		}
	}
}
