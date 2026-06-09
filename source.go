package previewer

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type preparedSource struct {
	path    string
	cleanup func()
}

func prepareSource(ctx context.Context, src Source) (preparedSource, error) {
	if src.Path != "" {
		return preparedSource{path: src.Path}, nil
	}
	if src.ReadSeeker == nil {
		return preparedSource{}, errors.New("source must have Path or ReadSeeker")
	}
	if err := checkContext(ctx); err != nil {
		return preparedSource{}, err
	}

	_, _ = src.ReadSeeker.Seek(0, io.SeekStart)
	pattern := "previewer-*"
	if ext := filepath.Ext(src.Name); ext != "" && !strings.ContainsAny(ext, `/\\`) {
		pattern += ext
	}
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return preparedSource{}, err
	}
	path := f.Name()
	cleanup := func() { _ = os.Remove(path) }

	_, copyErr := io.Copy(f, src.ReadSeeker)
	closeErr := f.Close()
	_, _ = src.ReadSeeker.Seek(0, io.SeekStart)
	if copyErr != nil {
		cleanup()
		return preparedSource{}, copyErr
	}
	if closeErr != nil {
		cleanup()
		return preparedSource{}, closeErr
	}
	return preparedSource{path: path, cleanup: cleanup}, nil
}
