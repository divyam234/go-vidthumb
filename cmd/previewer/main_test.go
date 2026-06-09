package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCLIEndToEnd(t *testing.T) {
	input := os.Getenv("PREVIEWER_TEST_INPUT")
	if input == "" {
		input = filepath.Join("..", "..", "testdata", "sample.mp4")
	}
	if _, err := os.Stat(input); err != nil {
		t.Skipf("test input not available: %s", input)
	}

	out := t.TempDir()
	cmd := exec.Command("go", "run", ".",
		"--input", input,
		"--out", out,
		"--workers", "2",
		"--cols", "2",
		"--rows", "2",
		"--thumb-width", "96",
		"--preview-slices", "2",
		"--slice-seconds", "0.75",
		"--preview-width", "0",
	)
	cmd.Dir = "."
	cmd.Env = os.Environ()
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cli failed: %v\n%s", err, b)
	}
	for _, name := range []string{"sprite.jpg", "sprite.vtt", "preview.mp4"} {
		p := filepath.Join(out, name)
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("missing %s: %v\n%s", name, err, b)
		}
		if st.Size() == 0 {
			t.Fatalf("empty %s", name)
		}
	}
}
