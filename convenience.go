package previewer

import (
	"context"
	"io"
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
