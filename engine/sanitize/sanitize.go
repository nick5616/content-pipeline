// Package sanitize strips location, device and owner metadata from media,
// losslessly, and then verifies the result. Verify is a hard gate: a Verified
// value can only come out of Verify, and the blob store only accepts
// Verified, so unsanitized bytes can't reach a blob by construction.
package sanitize

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nick5616/canon/engine/media"
)

// Runner runs an external tool and returns its stdout. Tests swap in a
// recorder; the default runs the real binary with a timeout and no shell.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ToolError carries a tool's stderr so ingest can show it.
type ToolError struct {
	Tool   string
	Args   []string
	Stderr string
	Err    error
}

func (e *ToolError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = e.Err.Error()
	}
	// exiftool ends messages with " - <path>"; the path is a staging file
	// that means nothing to the reader.
	if i := strings.LastIndex(msg, " - /"); i > 0 {
		msg = msg[:i]
	}
	return fmt.Sprintf("%s: %s", e.Tool, msg)
}

func (e *ToolError) Unwrap() error { return e.Err }

// Exec is the default Runner.
func Exec(timeout time.Duration) Runner {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		if err := cmd.Run(); err != nil {
			return stdout.Bytes(), &ToolError{Tool: name, Args: args, Stderr: stderr.String(), Err: err}
		}
		return stdout.Bytes(), nil
	}
}

// Sanitizer writes a stripped copy of in to out. It never modifies in.
type Sanitizer interface {
	Sanitize(ctx context.Context, t media.Type, in, out string) error
}

// Tools is the real Sanitizer: exiftool, ffmpeg, qpdf and a Go PNG filter.
type Tools struct {
	Run Runner
}

// NewTools returns Tools with the default runner (10-minute timeout, for
// long videos; stream copy is fast, so this is a hang guard).
func NewTools() Tools { return Tools{Run: Exec(10 * time.Minute)} }

// exiftoolBase disables user config, so ~/.ExifTool_config can't change
// output, and silences warnings (errors still fail the run).
var exiftoolBase = []string{"-config", "", "-q", "-q"}

func (s Tools) Sanitize(ctx context.Context, t media.Type, in, out string) error {
	if _, err := os.Stat(out); err == nil {
		return fmt.Errorf("sanitize: %s already exists", out)
	}
	// exiftool picks the format from the extension, so a JPEG named .HEIC
	// fails. Give the input its real extension (a hard link, so no copy).
	if filepath.Ext(in) != "."+t.Ext {
		named := out + ".in." + t.Ext
		if err := os.Link(in, named); err != nil {
			return fmt.Errorf("sanitize: link input: %w", err)
		}
		defer os.Remove(named)
		in = named
	}
	switch t.Family {
	case media.FamilyExifImage:
		return s.exifImage(ctx, in, out)
	case media.FamilyPNG:
		return sanitizePNG(in, out)
	case media.FamilyAV:
		return s.av(ctx, t, in, out)
	case media.FamilyPDF:
		return s.pdf(ctx, in, out)
	}
	return fmt.Errorf("sanitize: no sanitizer for %s", t.Name)
}

// tiffIFD0 are descriptive tags that `-all=` leaves in a TIFF, because there
// IFD0 is the image's own directory. Found by the fixtures: Artist,
// ImageDescription, Make, Model and Software all survived.
var tiffIFD0 = []string{
	"Artist", "ImageDescription", "Make", "Model", "Software", "Copyright", "DateTime",
	"HostComputer", "DocumentName", "PageName", "XPAuthor", "XPComment", "XPKeywords",
	"XPSubject", "XPTitle", "Rating", "RatingPercent",
}

// exifImage drops every tag, then copies back only orientation and the color
// profile from the original.
func (s Tools) exifImage(ctx context.Context, in, out string) error {
	args := append([]string{}, exiftoolBase...)
	args = append(args, "-all=")
	for _, tag := range tiffIFD0 {
		args = append(args, "-IFD0:"+tag+"=")
	}
	args = append(args, "-tagsFromFile", "@", "-ColorSpaceTags", "-Orientation", "-o", out, in)
	_, err := s.Run(ctx, "exiftool", args...)
	return err
}

// av copies the audio and video packets into a new container with no
// metadata, chapters, data tracks, subtitles or cover art. bitexact zeroes
// the container's creation dates and keeps ffmpeg's version string out, so
// the output depends only on the input and the ffmpeg version.
func (s Tools) av(ctx context.Context, t media.Type, in, out string) error {
	args := []string{
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-protocol_whitelist", "file,pipe",
		"-f", t.Demuxer, "-i", in,
	}
	if t.Kind == media.Video {
		// 0:V excludes attached pictures (cover art); data and subtitle
		// tracks are never mapped.
		args = append(args, "-map", "0:V", "-map", "0:a?")
	} else {
		args = append(args, "-map", "0:a")
	}
	args = append(args,
		"-map_metadata", "-1", "-map_chapters", "-1", "-c", "copy",
		"-fflags", "+bitexact", "-flags:v", "+bitexact", "-flags:a", "+bitexact")
	if t.Muxer == "mp3" || t.Muxer == "aiff" {
		args = append(args, "-write_id3v2", "0")
	}
	args = append(args, "-f", t.Muxer, out)
	_, err := s.Run(ctx, "ffmpeg", args...)
	return err
}

// pdf deletes metadata with exiftool, whose PDF edits are incremental and
// reversible, then has qpdf rewrite the file so the old objects are gone.
func (s Tools) pdf(ctx context.Context, in, out string) error {
	mid := out + ".exif.pdf"
	defer os.Remove(mid)
	args := append(append([]string{}, exiftoolBase...), "-all:all=", "-o", mid, in)
	if _, err := s.Run(ctx, "exiftool", args...); err != nil {
		return err
	}
	_, err := s.Run(ctx, "qpdf", "--deterministic-id", "--object-streams=generate",
		"--remove-unreferenced-resources=yes", "--warning-exit-0", mid, out)
	return err
}
