package previewer

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
)

func GenerateFromFile(ctx context.Context, path string, outputs OutputPaths, opts Options) (Result, error) {
	return Generate(ctx, FromFile(path), outputs, opts)
}

func GenerateFromReadSeeker(ctx context.Context, name string, r io.ReadSeeker, outputs OutputPaths, opts Options) (Result, error) {
	return Generate(ctx, FromReadSeeker(name, r), outputs, opts)
}

func GeneratePreviewFromFile(ctx context.Context, path, previewPath string, opts PreviewOptions, workers int) (*PreviewResult, MediaInfo, error) {
	return GeneratePreview(ctx, FromFile(path), previewPath, opts, workers)
}

func GeneratePreviewFromReadSeeker(ctx context.Context, name string, r io.ReadSeeker, previewPath string, opts PreviewOptions, workers int) (*PreviewResult, MediaInfo, error) {
	return GeneratePreview(ctx, FromReadSeeker(name, r), previewPath, opts, workers)
}

func GenerateSpriteFromFile(ctx context.Context, path, spritePath, vttPath string, opts SpriteOptions, workers int) (*SpriteResult, MediaInfo, error) {
	return GenerateSprite(ctx, FromFile(path), spritePath, vttPath, opts, workers)
}

func GenerateSpriteFromReadSeeker(ctx context.Context, name string, r io.ReadSeeker, spritePath, vttPath string, opts SpriteOptions, workers int) (*SpriteResult, MediaInfo, error) {
	return GenerateSprite(ctx, FromReadSeeker(name, r), spritePath, vttPath, opts, workers)
}

func ExtractThumbnailsFromFile(ctx context.Context, path string, opts SpriteOptions, workers int) ([]Thumb, MediaInfo, error) {
	return ExtractThumbnails(ctx, FromFile(path), opts, workers)
}

func ExtractThumbnailsFromReadSeeker(ctx context.Context, name string, r io.ReadSeeker, opts SpriteOptions, workers int) ([]Thumb, MediaInfo, error) {
	return ExtractThumbnails(ctx, FromReadSeeker(name, r), opts, workers)
}

func CalculatePHashFromFile(ctx context.Context, path string, opts PHashOptions, workers int) (*PHashResult, MediaInfo, error) {
	return CalculatePHash(ctx, FromFile(path), opts, workers)
}

func CalculatePHashFromReadSeeker(ctx context.Context, name string, r io.ReadSeeker, opts PHashOptions, workers int) (*PHashResult, MediaInfo, error) {
	return CalculatePHash(ctx, FromReadSeeker(name, r), opts, workers)
}

func ProbeFile(ctx context.Context, path string) (MediaInfo, error) {
	return ProbeSource(ctx, FromFile(path))
}

func ProbeReadSeeker(ctx context.Context, name string, r io.ReadSeeker) (MediaInfo, error) {
	return ProbeSource(ctx, FromReadSeeker(name, r))
}

// RemuxFromFile copies video and audio streams into a new container without
// decoding or re-encoding and enables faststart for MP4 output.
func RemuxFromFile(ctx context.Context, inputPath, outputPath string) error {
	if inputPath == "" {
		return errors.New("inputPath is required")
	}
	if outputPath == "" {
		return errors.New("outputPath is required")
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	inputAbs, err := filepath.Abs(inputPath)
	if err != nil {
		return err
	}
	outputAbs, err := filepath.Abs(outputPath)
	if err != nil {
		return err
	}
	if inputAbs == outputAbs {
		return errors.New("inputPath and outputPath must differ")
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return err
	}
	if err := remuxFile(inputPath, outputPath); err != nil {
		_ = os.Remove(outputPath)
		return err
	}
	return nil
}
