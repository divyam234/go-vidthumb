package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendWithLDLibraryPathPrependsLibrary(t *testing.T) {
	env := appendWithLDLibraryPath([]string{"A=B", "LD_LIBRARY_PATH=/old"}, "/new")
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "LD_LIBRARY_PATH=/new:/old") {
		t.Fatalf("LD_LIBRARY_PATH not prepended: %q", env)
	}
}

func TestFFmpegCmdRunsFromBinaryDirectory(t *testing.T) {
	cmd := ffmpegCmd(filepath.Join("/tmp", "ffmpeg-8.1", "bin", "ffmpeg"), "/tmp/ffmpeg-8.1/lib", "-version")
	if got, want := cmd.Dir, filepath.Join("/tmp", "ffmpeg-8.1", "bin"); got != want {
		t.Fatalf("cmd.Dir=%q want %q", got, want)
	}
	found := false
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "LD_LIBRARY_PATH=/tmp/ffmpeg-8.1/lib") {
			found = true
		}
	}
	if !found && os.Getenv("LD_LIBRARY_PATH") == "" {
		t.Fatalf("ffmpeg lib path not present in env: %q", cmd.Env)
	}
}
