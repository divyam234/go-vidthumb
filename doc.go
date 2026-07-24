// Package previewer generates video preview assets using FFmpeg shared
// libraries through cgo.
//
// It provides independent APIs for probing media, extracting seek-based
// thumbnails, writing sprite/VTT files, generating short preview videos from
// parallel stream-copy slices, remuxing complete files without re-encoding,
// and calculating API-only perceptual hashes.
//
// The package never shells out to ffmpeg or ffprobe at runtime. The tests may
// optionally execute the ffmpeg CLI as a reference implementation to verify
// behavior.
package previewer
