package toolchain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestParseVersion(t *testing.T) {
	tests := []struct{ tool, file, want string }{
		{"ffmpeg", "ffmpeg-6.1-ubuntu.txt", "6.1.1-3ubuntu5"},
		{"ffmpeg", "ffmpeg-8.0-brew.txt", "8.0"},
		{"ffmpeg", "ffmpeg-git-static.txt", "N-121000-g1a2b3c4d5e-static"},
		{"ffprobe", "ffprobe-6.1-ubuntu.txt", "6.1.1-3ubuntu5"},
		{"ffprobe", "ffprobe-8.0-brew.txt", "8.0"},
		{"exiftool", "exiftool-12.76-ubuntu.txt", "12.76"},
		{"exiftool", "exiftool-13.36-brew.txt", "13.36"},
		{"qpdf", "qpdf-11.9-ubuntu.txt", "11.9.0"},
		{"qpdf", "qpdf-12.2-brew.txt", "12.2.0"},
		{"heif-dec", "heif-dec-1.17-ubuntu.txt", "1.17.6"},
		{"heif-dec", "heif-dec-1.20-brew.txt", "1.20.2"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			out, err := os.ReadFile(filepath.Join("testdata", tt.file))
			if err != nil {
				t.Fatal(err)
			}
			got, err := ParseVersion(tt.tool, out)
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestParseVersionRejectsGarbage(t *testing.T) {
	for _, tool := range Required {
		if _, err := ParseVersion(tool, []byte("command not found\n")); err == nil {
			t.Errorf("%s: want error", tool)
		}
	}
	// ffprobe output must not satisfy ffmpeg.
	out, _ := os.ReadFile(filepath.Join("testdata", "ffprobe-6.1-ubuntu.txt"))
	if _, err := ParseVersion("ffmpeg", out); err == nil {
		t.Error("ffmpeg parser accepted ffprobe output")
	}
}

func fakeFinder(path string, out string) Finder {
	return Finder{
		LookPath: func(string) (string, error) { return path, nil },
		Run:      func(context.Context, string, ...string) ([]byte, error) { return []byte(out), nil },
	}
}

func TestLookup(t *testing.T) {
	ctx := context.Background()

	tool, err := fakeFinder("/usr/bin/qpdf", "qpdf version 11.9.0\n").Lookup(ctx, "qpdf")
	if err != nil || tool != (Tool{"qpdf", "/usr/bin/qpdf", "11.9.0"}) {
		t.Fatalf("got %+v, %v", tool, err)
	}

	_, err = fakeFinder("/mnt/c/Program Files/qpdf/bin/qpdf.exe", "qpdf version 11.9.0\n").Lookup(ctx, "qpdf")
	if !errors.Is(err, ErrWindowsBinary) {
		t.Fatalf("want ErrWindowsBinary, got %v", err)
	}

	missing := Finder{LookPath: func(string) (string, error) { return "", errors.New("nope") }}
	if _, err := missing.Lookup(ctx, "ffmpeg"); err == nil {
		t.Fatal("want not-found error")
	}

	if _, err := (Finder{}).Lookup(ctx, "rm"); err == nil {
		t.Fatal("want unknown-tool error")
	}
}
