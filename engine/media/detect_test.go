package media

import (
	"bytes"
	"errors"
	"testing"
)

func ftyp(major string, compat ...string) []byte {
	var b bytes.Buffer
	size := 16 + 4*len(compat)
	b.Write([]byte{0, 0, 0, byte(size)})
	b.WriteString("ftyp" + major + "\x00\x00\x00\x00")
	for _, c := range compat {
		b.WriteString(c)
	}
	return append(b.Bytes(), make([]byte, 32)...)
}

func TestDetect(t *testing.T) {
	tests := []struct {
		name string
		head []byte
		want Type
	}{
		{"jpeg", []byte("\xFF\xD8\xFF\xE0\x00\x10JFIF"), JPEG},
		{"png", []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR"), PNG},
		{"gif", []byte("GIF89a\x01\x00"), GIF},
		{"webp", []byte("RIFF\x10\x00\x00\x00WEBPVP8 "), WebP},
		{"wav", []byte("RIFF\x10\x00\x00\x00WAVEfmt "), WAV},
		{"tiff le", []byte("II*\x00\x08\x00\x00\x00"), TIFF},
		{"tiff be", []byte("MM\x00*\x00\x00\x00\x08"), TIFF},
		{"aiff", []byte("FORM\x00\x00\x10\x00AIFFCOMM"), AIFF},
		{"aifc", []byte("FORM\x00\x00\x10\x00AIFCFVER"), AIFF},
		{"flac", []byte("fLaC\x00\x00\x00\x22"), FLAC},
		{"pdf", []byte("%PDF-1.7\n"), PDF},
		{"mp3 id3", []byte("ID3\x04\x00\x00"), MP3},
		{"mp3 frame", []byte{0xFF, 0xFB, 0x90, 0x64, 0, 0}, MP3},
		{"heic", ftyp("heic", "mif1", "heic"), HEIC},
		{"heif mif1", ftyp("mif1", "mif1", "heic"), HEIC},
		{"avif", ftyp("avif", "mif1", "avif"), AVIF},
		{"avif via mif1", ftyp("mif1", "mif1", "avif"), AVIF},
		{"mov", ftyp("qt  ", "qt  "), MOV},
		{"old mov", []byte("\x00\x00\x00\x08wide\x00\x00\x00\x00mdat"), MOV},
		{"mp4", ftyp("isom", "isom", "iso2", "avc1", "mp41"), MP4},
		{"m4v", ftyp("M4V ", "M4V ", "M4A ", "mp42"), MP4},
		{"m4a", ftyp("M4A ", "M4A ", "mp42", "isom"), M4A},
	}
	for _, tt := range tests {
		got, err := Detect(bytes.NewReader(tt.head))
		if err != nil || got != tt.want {
			t.Errorf("%s: got %v, %v; want %s", tt.name, got.Name, err, tt.want.Name)
		}
	}
}

func TestDetectRejects(t *testing.T) {
	tests := []struct{ name, head, what string }{
		{"playlist", "#EXTM3U\n#EXT-X-VERSION:3\n", "HLS playlist"},
		{"playlist with bom", "\xEF\xBB\xBF#EXTM3U\n", "HLS playlist"},
		{"zip", "PK\x03\x04\x14\x00", "ZIP"},
		{"html", "<!doctype html>", "markup"},
		{"svg", "<svg xmlns=", "markup"},
		{"concat", "ffconcat version 1.0\nfile '/etc/passwd'", "concat"},
		{"text", "hello world", "unrecognized"},
		{"unknown ftyp", string(ftyp("crx ", "crx ")), "brand"},
		{"not mp3", "\xFF\xFF\xFF\xFF", "unrecognized"},
	}
	for _, tt := range tests {
		_, err := Detect(bytes.NewReader([]byte(tt.head)))
		var ue *UnsupportedError
		if !errors.As(err, &ue) || !bytes.Contains([]byte(ue.What), []byte(tt.what)) {
			t.Errorf("%s: got %v, want unsupported mentioning %q", tt.name, err, tt.what)
		}
	}
	if _, err := Detect(bytes.NewReader(nil)); !errors.Is(err, ErrEmpty) {
		t.Errorf("empty: %v", err)
	}
}
