// Package media identifies files by their contents. Extensions lie (the
// artandmedia bucket had JPEGs named .HEIC), so nothing downstream ever
// looks at one.
package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
)

// Kind is the broad class of a file.
type Kind string

const (
	Image    Kind = "image"
	Video    Kind = "video"
	Audio    Kind = "audio"
	Document Kind = "document"
)

// Family picks the sanitizer.
type Family string

const (
	FamilyExifImage Family = "exif-image" // exiftool strips in place
	FamilyPNG       Family = "png"        // Go chunk allowlist, then exiftool
	FamilyAV        Family = "av"         // ffmpeg stream copy
	FamilyPDF       Family = "pdf"        // exiftool, then qpdf rewrite
)

// Type is a supported file type.
type Type struct {
	Name   string // short id: jpeg, png, heic, mov, mp3, ...
	Kind   Kind
	Family Family
	MIME   string
	Ext    string // canonical extension, without the dot
	// Demuxer and Muxer are ffmpeg format names for FamilyAV.
	Demuxer, Muxer string
}

var (
	JPEG = Type{Name: "jpeg", Kind: Image, Family: FamilyExifImage, MIME: "image/jpeg", Ext: "jpg"}
	PNG  = Type{Name: "png", Kind: Image, Family: FamilyPNG, MIME: "image/png", Ext: "png"}
	GIF  = Type{Name: "gif", Kind: Image, Family: FamilyExifImage, MIME: "image/gif", Ext: "gif"}
	WebP = Type{Name: "webp", Kind: Image, Family: FamilyExifImage, MIME: "image/webp", Ext: "webp"}
	TIFF = Type{Name: "tiff", Kind: Image, Family: FamilyExifImage, MIME: "image/tiff", Ext: "tif"}
	HEIC = Type{Name: "heic", Kind: Image, Family: FamilyExifImage, MIME: "image/heic", Ext: "heic"}
	AVIF = Type{Name: "avif", Kind: Image, Family: FamilyExifImage, MIME: "image/avif", Ext: "avif"}
	MOV  = Type{Name: "mov", Kind: Video, Family: FamilyAV, MIME: "video/quicktime", Ext: "mov", Demuxer: "mov", Muxer: "mov"}
	MP4  = Type{Name: "mp4", Kind: Video, Family: FamilyAV, MIME: "video/mp4", Ext: "mp4", Demuxer: "mov", Muxer: "mp4"}
	M4A  = Type{Name: "m4a", Kind: Audio, Family: FamilyAV, MIME: "audio/mp4", Ext: "m4a", Demuxer: "mov", Muxer: "ipod"}
	MP3  = Type{Name: "mp3", Kind: Audio, Family: FamilyAV, MIME: "audio/mpeg", Ext: "mp3", Demuxer: "mp3", Muxer: "mp3"}
	AIFF = Type{Name: "aiff", Kind: Audio, Family: FamilyAV, MIME: "audio/aiff", Ext: "aiff", Demuxer: "aiff", Muxer: "aiff"}
	WAV  = Type{Name: "wav", Kind: Audio, Family: FamilyAV, MIME: "audio/wav", Ext: "wav", Demuxer: "wav", Muxer: "wav"}
	FLAC = Type{Name: "flac", Kind: Audio, Family: FamilyAV, MIME: "audio/flac", Ext: "flac", Demuxer: "flac", Muxer: "flac"}
	PDF  = Type{Name: "pdf", Kind: Document, Family: FamilyPDF, MIME: "application/pdf", Ext: "pdf"}
)

// Supported lists every type ingest accepts.
var Supported = []Type{JPEG, PNG, GIF, WebP, TIFF, HEIC, AVIF, MOV, MP4, M4A, MP3, AIFF, WAV, FLAC, PDF}

// UnsupportedError names what a rejected file looks like.
type UnsupportedError struct{ What string }

func (e *UnsupportedError) Error() string { return "unsupported file type: " + e.What }

// ErrEmpty is returned for zero-byte input.
var ErrEmpty = errors.New("file is empty")

// sniffLen is how much of the head Detect reads.
const sniffLen = 4096

// Detect identifies r from its first bytes.
func Detect(r io.ReaderAt) (Type, error) {
	head := make([]byte, sniffLen)
	n, err := r.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return Type{}, err
	}
	head = head[:n]
	if n == 0 {
		return Type{}, ErrEmpty
	}
	return detect(head)
}

func detect(b []byte) (Type, error) {
	has := func(off int, sig string) bool {
		return len(b) >= off+len(sig) && string(b[off:off+len(sig)]) == sig
	}
	switch {
	case has(0, "\xFF\xD8\xFF"):
		return JPEG, nil
	case has(0, "\x89PNG\r\n\x1a\n"):
		return PNG, nil
	case has(0, "GIF87a"), has(0, "GIF89a"):
		return GIF, nil
	case has(0, "RIFF") && has(8, "WEBP"):
		return WebP, nil
	case has(0, "RIFF") && has(8, "WAVE"):
		return WAV, nil
	case has(0, "II*\x00"), has(0, "MM\x00*"):
		return TIFF, nil
	case has(0, "FORM") && (has(8, "AIFF") || has(8, "AIFC")):
		return AIFF, nil
	case has(0, "fLaC"):
		return FLAC, nil
	case has(0, "%PDF-"):
		return PDF, nil
	case has(0, "ID3"):
		return MP3, nil
	case has(4, "ftyp"):
		return isoBMFF(b)
	case has(4, "moov"), has(4, "mdat"), has(4, "wide"), has(4, "free"), has(4, "skip"):
		return MOV, nil // QuickTime files from before ftyp existed
	case mp3Frame(b):
		return MP3, nil
	}
	return Type{}, &UnsupportedError{What: describe(b)}
}

// isoBMFF tells HEIC, AVIF, MOV, MP4 and M4A apart by their ftyp brands.
func isoBMFF(b []byte) (Type, error) {
	size := int(binary.BigEndian.Uint32(b[0:4]))
	if size < 16 || size > len(b) {
		size = min(len(b), 64)
	}
	major := string(b[8:12])
	brands := []string{major}
	for off := 16; off+4 <= size; off += 4 {
		brands = append(brands, string(b[off:off+4]))
	}
	hasBrand := func(want ...string) bool {
		for _, w := range want {
			if slices.Contains(brands, w) {
				return true
			}
		}
		return false
	}
	switch {
	case major == "avif" || major == "avis":
		return AVIF, nil
	case hasBrand("heic", "heix", "heim", "heis", "hevc", "hevx"):
		return HEIC, nil
	case major == "mif1" || major == "msf1":
		if hasBrand("avif", "avis") {
			return AVIF, nil
		}
		return HEIC, nil
	case major == "qt  ":
		return MOV, nil
	case major == "M4A " || major == "M4B ":
		return M4A, nil
	case hasBrand("isom", "iso2", "iso4", "iso5", "iso6", "mp41", "mp42", "avc1", "M4V ", "M4VP", "dash", "3gp4", "3gp5", "3g2a"):
		return MP4, nil
	}
	return Type{}, &UnsupportedError{What: fmt.Sprintf("ISO media with brand %q", major)}
}

// mp3Frame recognizes an MPEG audio frame header without an ID3 tag.
func mp3Frame(b []byte) bool {
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return false
	}
	version := (b[1] >> 3) & 3
	layer := (b[1] >> 1) & 3
	bitrate := b[2] >> 4
	rate := (b[2] >> 2) & 3
	return version != 1 && layer != 0 && bitrate != 0 && bitrate != 15 && rate != 3
}

// describe names common things people might drop that Canon rejects.
func describe(b []byte) string {
	t := bytes.TrimLeft(b, "\xEF\xBB\xBF \t\r\n")
	switch {
	case bytes.HasPrefix(t, []byte("#EXTM3U")):
		return "an HLS playlist"
	case bytes.HasPrefix(b, []byte("PK\x03\x04")):
		return "a ZIP archive"
	case bytes.HasPrefix(t, []byte("<")):
		return "markup (HTML, SVG or XML)"
	case bytes.HasPrefix(b, []byte("\x1f\x8b")):
		return "a gzip file"
	case bytes.HasPrefix(t, []byte("ffconcat")):
		return "an ffmpeg concat script"
	}
	n := min(len(b), 8)
	return fmt.Sprintf("unrecognized content starting % x", b[:n])
}
