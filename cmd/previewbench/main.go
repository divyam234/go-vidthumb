package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	previewer "media-previewer"

	"github.com/spf13/pflag"
)

const appVersion = "0.1.0"

type runMetrics struct {
	Name       string
	Wall       time.Duration
	User       time.Duration
	System     time.Duration
	MaxRSSKB   int64
	OutputSize int64
	RawSHA256  string
	FrameBytes int64
	Commands   int
}

type measuredCommand struct {
	Wall     time.Duration
	User     time.Duration
	System   time.Duration
	MaxRSSKB int64
	Stdout   []byte
	Stderr   []byte
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "__api-worker" {
		if err := runAPIWorker(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	if err := runBench(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runBench(args []string) error {
	fs := pflag.NewFlagSet("previewbench", pflag.ExitOnError)
	input := fs.StringP("input", "i", "", "input video path")
	outDir := fs.StringP("out", "o", "bench-out", "benchmark output directory")
	ffmpegPath := fs.String("ffmpeg", defaultFFmpegPath(), "FFmpeg CLI path to use for the reference benchmark")
	libPath := fs.String("ffmpeg-lib", defaultFFmpegLibPath(), "directory containing FFmpeg shared libraries for the CLI benchmark")
	workers := fs.IntP("workers", "j", min(max(runtime.NumCPU()/2, 1), 4), "parallel slice workers")
	slices := fs.Int("preview-slices", 12, "number of preview slices")
	sliceSeconds := fs.Float64("slice-seconds", 2.5, "seconds per preview slice")
	width := fs.Int("preview-width", 640, "final preview width")
	keep := fs.Bool("keep", false, "keep existing output directory instead of recreating it")
	version := fs.BoolP("version", "v", false, "print version and exit")

	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Benchmark media-previewer Go API against a real FFmpeg CLI reference.\n\n")
		fmt.Fprintf(fs.Output(), "The two paths are run sequentially, not at the same time, so each gets the full machine.\n\n")
		fmt.Fprintf(fs.Output(), "Usage:\n  %s --input video.mp4 --ffmpeg /path/to/ffmpeg --out bench-out [flags]\n\n", os.Args[0])
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *version {
		fmt.Println(appVersion)
		return nil
	}
	if *input == "" {
		fs.Usage()
		return errors.New("missing --input")
	}
	absInput, err := filepath.Abs(*input)
	if err != nil {
		return err
	}
	if _, err := os.Stat(absInput); err != nil {
		return fmt.Errorf("input unavailable: %w", err)
	}
	if _, err := os.Stat(*ffmpegPath); err != nil {
		return fmt.Errorf("ffmpeg unavailable at %q: %w", *ffmpegPath, err)
	}
	if *workers <= 0 {
		*workers = 1
	}
	if *slices <= 0 {
		return errors.New("--preview-slices must be positive")
	}
	if *sliceSeconds <= 0 {
		return errors.New("--slice-seconds must be positive")
	}
	if !*keep {
		_ = os.RemoveAll(*outDir)
	}
	if err := os.MkdirAll(*outDir, 0755); err != nil {
		return err
	}

	info, err := previewer.ProbeFile(context.Background(), absInput)
	if err != nil {
		return fmt.Errorf("probe input: %w", err)
	}
	fmt.Printf("input: %s\n", absInput)
	fmt.Printf("probe: duration=%.3fs size=%dx%d fps=%.3f\n", info.Duration, info.Width, info.Height, info.FPS)
	fmt.Printf("options: slices=%d slice_seconds=%.3f width=%d workers=%d\n", *slices, *sliceSeconds, *width, *workers)
	fmt.Printf("ffmpeg: %s\n", *ffmpegPath)
	if *libPath != "" {
		fmt.Printf("ffmpeg_lib: %s\n", *libPath)
	}
	fmt.Println("note: runs are sequential; Go API finishes before FFmpeg CLI starts.")
	fmt.Println()

	opts := previewer.PreviewOptions{Enabled: true, Slices: *slices, SliceSeconds: *sliceSeconds, Width: *width, KeepParts: true}
	apiOut := filepath.Join(*outDir, "go-api")
	cliOut := filepath.Join(*outDir, "ffmpeg-cli")

	api, err := runGoAPIBenchmark(absInput, apiOut, opts, *workers, *ffmpegPath, *libPath)
	if err != nil {
		return err
	}
	// Give the system a tiny quiet period so the second benchmark does not run on top
	// of immediate process teardown and filesystem cleanup from the first one.
	time.Sleep(300 * time.Millisecond)
	cli, err := runFFmpegCLIBenchmark(absInput, cliOut, info, opts, *workers, *ffmpegPath, *libPath)
	if err != nil {
		return err
	}

	printTable([]runMetrics{api, cli})
	fmt.Println()
	if api.RawSHA256 == cli.RawSHA256 && api.FrameBytes == cli.FrameBytes {
		fmt.Printf("decoded-frame parity: PASS sha256=%s bytes=%d\n", api.RawSHA256, api.FrameBytes)
	} else {
		fmt.Printf("decoded-frame parity: FAIL\n  go-api     sha256=%s bytes=%d\n  ffmpeg-cli sha256=%s bytes=%d\n", api.RawSHA256, api.FrameBytes, cli.RawSHA256, cli.FrameBytes)
	}
	winner := "Go API"
	if cli.Wall < api.Wall {
		winner = "FFmpeg CLI"
	}
	fmt.Printf("wall-time winner: %s\n", winner)
	if cli.MaxRSSKB < api.MaxRSSKB {
		fmt.Printf("peak-RSS winner: FFmpeg CLI (%s vs %s)\n", formatKB(cli.MaxRSSKB), formatKB(api.MaxRSSKB))
	} else if api.MaxRSSKB < cli.MaxRSSKB {
		fmt.Printf("peak-RSS winner: Go API (%s vs %s)\n", formatKB(api.MaxRSSKB), formatKB(cli.MaxRSSKB))
	} else {
		fmt.Println("peak-RSS winner: tie")
	}
	return writeMarkdownReport(filepath.Join(*outDir, "benchmark-report.md"), []runMetrics{api, cli}, api.RawSHA256 == cli.RawSHA256 && api.FrameBytes == cli.FrameBytes)
}

func runAPIWorker(args []string) error {
	fs := pflag.NewFlagSet("api-worker", pflag.ExitOnError)
	input := fs.String("input", "", "input path")
	out := fs.String("out", "", "output preview path")
	workers := fs.Int("workers", 4, "workers")
	slices := fs.Int("preview-slices", 12, "slices")
	sliceSeconds := fs.Float64("slice-seconds", 2.5, "slice seconds")
	width := fs.Int("preview-width", 640, "preview width")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" || *out == "" {
		return errors.New("api worker requires --input and --out")
	}
	opts := previewer.PreviewOptions{Enabled: true, Slices: *slices, SliceSeconds: *sliceSeconds, Width: *width, KeepParts: true}
	_, _, err := previewer.GeneratePreviewFromFile(context.Background(), *input, *out, opts, *workers)
	return err
}

func runGoAPIBenchmark(input, outDir string, opts previewer.PreviewOptions, workers int, ffmpegPath, ffmpegLib string) (runMetrics, error) {
	if err := os.RemoveAll(outDir); err != nil {
		return runMetrics{}, err
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return runMetrics{}, err
	}
	exe, err := os.Executable()
	if err != nil {
		return runMetrics{}, err
	}
	previewPath := filepath.Join(outDir, "preview.mp4")
	cmd := exec.Command(exe,
		"__api-worker",
		"--input", input,
		"--out", previewPath,
		"--workers", strconv.Itoa(workers),
		"--preview-slices", strconv.Itoa(opts.Slices),
		"--slice-seconds", strconv.FormatFloat(opts.SliceSeconds, 'f', 9, 64),
		"--preview-width", strconv.Itoa(opts.Width),
	)
	cmd.Env = os.Environ()
	m, err := runMeasured(cmd)
	if err != nil {
		return runMetrics{}, fmt.Errorf("go api benchmark failed: %w\n%s", err, string(m.Stderr))
	}
	return finalizeMetrics("Go API", previewPath, 1, m.Wall, m.User, m.System, m.MaxRSSKB, ffmpegPath, ffmpegLib)
}

func runFFmpegCLIBenchmark(input, outDir string, info previewer.MediaInfo, opts previewer.PreviewOptions, workers int, ffmpegPath, ffmpegLib string) (runMetrics, error) {
	if err := os.RemoveAll(outDir); err != nil {
		return runMetrics{}, err
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return runMetrics{}, err
	}
	partsDir := filepath.Join(outDir, "parts")
	if err := os.MkdirAll(partsDir, 0755); err != nil {
		return runMetrics{}, err
	}
	if workers <= 0 {
		workers = 1
	}
	if workers > opts.Slices {
		workers = opts.Slices
	}
	sliceSeconds := opts.SliceSeconds
	if info.Duration > 0 && info.Duration < sliceSeconds {
		sliceSeconds = info.Duration
	}
	maxStart := 0.0
	if info.Duration > sliceSeconds {
		maxStart = info.Duration - sliceSeconds
	}
	parts := make([]string, opts.Slices)
	type job struct{ index int }
	type result struct {
		index int
		m     measuredCommand
		err   error
	}
	jobs := make(chan job, opts.Slices)
	results := make(chan result, opts.Slices)
	wallStart := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				start := 0.0
				if opts.Slices > 1 {
					start = (float64(j.index) / float64(opts.Slices-1)) * maxStart
				}
				part := filepath.Join(partsDir, fmt.Sprintf("part_%04d.mp4", j.index))
				cmd := ffmpegCmd(ffmpegPath, ffmpegLib,
					"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
					"-ss", strconv.FormatFloat(start, 'f', 9, 64),
					"-i", input,
					"-t", strconv.FormatFloat(sliceSeconds, 'f', 9, 64),
					"-an", "-c:v", "copy",
					part,
				)
				m, err := runMeasured(cmd)
				if err == nil {
					parts[j.index] = part
				}
				results <- result{index: j.index, m: m, err: err}
			}
		}()
	}
	for i := 0; i < opts.Slices; i++ {
		jobs <- job{index: i}
	}
	close(jobs)
	go func() {
		wg.Wait()
		close(results)
	}()

	var user, sys time.Duration
	var maxRSS int64
	commands := 0
	for r := range results {
		commands++
		user += r.m.User
		sys += r.m.System
		if r.m.MaxRSSKB > maxRSS {
			maxRSS = r.m.MaxRSSKB
		}
		if r.err != nil {
			return runMetrics{}, fmt.Errorf("ffmpeg slice %d failed: %w\n%s", r.index, r.err, string(r.m.Stderr))
		}
	}
	listPath := filepath.Join(outDir, "concat.txt")
	var list strings.Builder
	for _, part := range parts {
		abs, err := filepath.Abs(part)
		if err != nil {
			return runMetrics{}, err
		}
		list.WriteString("file '")
		list.WriteString(filepath.ToSlash(abs))
		list.WriteString("'\n")
	}
	if err := os.WriteFile(listPath, []byte(list.String()), 0644); err != nil {
		return runMetrics{}, err
	}
	filter := fmt.Sprintf("scale=%d:-2:flags=fast_bilinear,fps=%.12g", opts.Width, info.FPS)
	previewPath := filepath.Join(outDir, "preview.mp4")
	concatCmd := ffmpegCmd(ffmpegPath, ffmpegLib,
		"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-f", "concat", "-safe", "0", "-i", listPath,
		"-vf", filter,
		"-an", "-c:v", "libx264", "-preset", "ultrafast", "-crf", "28",
		"-pix_fmt", "yuv420p", "-bf", "0", "-g", "48",
		"-movflags", "+faststart",
		previewPath,
	)
	m, err := runMeasured(concatCmd)
	commands++
	user += m.User
	sys += m.System
	if m.MaxRSSKB > maxRSS {
		maxRSS = m.MaxRSSKB
	}
	if err != nil {
		return runMetrics{}, fmt.Errorf("ffmpeg concat failed: %w\n%s", err, string(m.Stderr))
	}
	wall := time.Since(wallStart)
	return finalizeMetrics("FFmpeg 8.1 CLI", previewPath, commands, wall, user, sys, maxRSS, ffmpegPath, ffmpegLib)
}

func finalizeMetrics(name, previewPath string, commands int, wall, user, sys time.Duration, maxRSSKB int64, ffmpegPath, ffmpegLib string) (runMetrics, error) {
	st, err := os.Stat(previewPath)
	if err != nil {
		return runMetrics{}, err
	}
	rawHash, rawBytes, err := decodedRawHash(ffmpegPath, ffmpegLib, previewPath)
	if err != nil {
		return runMetrics{}, err
	}
	return runMetrics{Name: name, Wall: wall, User: user, System: sys, MaxRSSKB: maxRSSKB, OutputSize: st.Size(), RawSHA256: rawHash, FrameBytes: rawBytes, Commands: commands}, nil
}

func decodedRawHash(ffmpegPath, ffmpegLib, input string) (string, int64, error) {
	cmd := ffmpegCmd(ffmpegPath, ffmpegLib,
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-i", input,
		"-an", "-f", "rawvideo", "-pix_fmt", "yuv420p", "-",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", 0, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, copyErr := io.Copy(h, stdout)
	waitErr := cmd.Wait()
	if copyErr != nil {
		return "", n, fmt.Errorf("decode raw frames read: %w\n%s", copyErr, stderr.String())
	}
	if waitErr != nil {
		return "", n, fmt.Errorf("decode raw frames: %w\n%s", waitErr, stderr.String())
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func ffmpegCmd(ffmpegPath, ffmpegLib string, args ...string) *exec.Cmd {
	cmd := exec.Command(ffmpegPath, args...)
	// The uploaded FFmpeg 8.1 binary has an RPATH that resolves ../lib relative
	// to its working directory, so run it from its own bin directory. This keeps
	// the benchmark on the uploaded 8.1 CLI instead of accidentally falling back
	// to the system FFmpeg.
	if dir := filepath.Dir(ffmpegPath); dir != "." && dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = os.Environ()
	if ffmpegLib != "" {
		cmd.Env = appendWithLDLibraryPath(cmd.Env, ffmpegLib)
	}
	return cmd
}

func appendWithLDLibraryPath(env []string, lib string) []string {
	out := make([]string, 0, len(env)+1)
	found := false
	for _, e := range env {
		if strings.HasPrefix(e, "LD_LIBRARY_PATH=") {
			found = true
			cur := strings.TrimPrefix(e, "LD_LIBRARY_PATH=")
			if cur == "" {
				out = append(out, "LD_LIBRARY_PATH="+lib)
			} else {
				out = append(out, "LD_LIBRARY_PATH="+lib+":"+cur)
			}
			continue
		}
		out = append(out, e)
	}
	if !found {
		out = append(out, "LD_LIBRARY_PATH="+lib)
	}
	return out
}

func runMeasured(cmd *exec.Cmd) (measuredCommand, error) {
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	start := time.Now()
	err := cmd.Run()
	wall := time.Since(start)
	m := measuredCommand{Wall: wall, Stdout: []byte(stdout.String()), Stderr: []byte(stderr.String())}
	if cmd.ProcessState != nil {
		if ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
			m.User = timevalDuration(ru.Utime)
			m.System = timevalDuration(ru.Stime)
			m.MaxRSSKB = ru.Maxrss
		}
	}
	return m, err
}

func timevalDuration(tv syscall.Timeval) time.Duration {
	return time.Duration(tv.Sec)*time.Second + time.Duration(tv.Usec)*time.Microsecond
}

func printTable(rows []runMetrics) {
	fmt.Println("| Case | Wall | User CPU | Sys CPU | CPU/wall | Max RSS | Output | Commands |")
	fmt.Println("|---|---:|---:|---:|---:|---:|---:|---:|")
	for _, r := range rows {
		cpu := r.User + r.System
		ratio := 0.0
		if r.Wall > 0 {
			ratio = float64(cpu) / float64(r.Wall)
		}
		fmt.Printf("| %s | %s | %s | %s | %.2fx | %s | %s | %d |\n", r.Name, r.Wall.Round(time.Millisecond), r.User.Round(time.Millisecond), r.System.Round(time.Millisecond), ratio, formatKB(r.MaxRSSKB), formatBytes(r.OutputSize), r.Commands)
	}
}

func writeMarkdownReport(path string, rows []runMetrics, parity bool) error {
	var b strings.Builder
	b.WriteString("# Preview benchmark report\n\n")
	b.WriteString("| Case | Wall | User CPU | Sys CPU | CPU/wall | Max RSS | Output | Commands | Raw SHA-256 |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|---|\n")
	for _, r := range rows {
		cpu := r.User + r.System
		ratio := 0.0
		if r.Wall > 0 {
			ratio = float64(cpu) / float64(r.Wall)
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %.2fx | %s | %s | %d | `%s` |\n", r.Name, r.Wall.Round(time.Millisecond), r.User.Round(time.Millisecond), r.System.Round(time.Millisecond), ratio, formatKB(r.MaxRSSKB), formatBytes(r.OutputSize), r.Commands, r.RawSHA256))
	}
	if parity {
		b.WriteString("\nDecoded-frame parity: PASS.\n")
	} else {
		b.WriteString("\nDecoded-frame parity: FAIL.\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0644)
}

func formatKB(kb int64) string {
	if kb <= 0 {
		return "n/a"
	}
	return formatBytes(kb * 1024)
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func defaultFFmpegPath() string {
	candidates := []string{
		filepath.Join(os.Getenv("FFMPEG_PREFIX"), "bin", "ffmpeg"),
		"/mnt/data/ffmpeg-8.1/bin/ffmpeg",
	}
	for _, c := range candidates {
		if c == "" || strings.HasPrefix(c, string(filepath.Separator)+"bin") {
			continue
		}
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	return "ffmpeg"
}

func defaultFFmpegLibPath() string {
	// Do not force LD_LIBRARY_PATH for the uploaded FFmpeg 8.1 build.
	// That archive contains its own libc/libm, and putting its lib directory in
	// LD_LIBRARY_PATH can make the dynamic loader pick those over the system libc.
	// The benchmark runner instead executes ffmpeg from its bin directory, where
	// the build's relative RPATH resolves the FFmpeg shared libraries correctly.
	return os.Getenv("PREVIEWER_FFMPEG_CLI_LIB")
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

// keep io imported when builds are trimmed by future edits
var _ io.Reader
