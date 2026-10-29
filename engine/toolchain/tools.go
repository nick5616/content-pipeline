// Package toolchain finds the external binaries Canon shells out to and
// records their versions, so a changed ffmpeg or exiftool shows up in doctor
// before it shows up as different output bytes.
package toolchain

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Tool is a resolved binary.
type Tool struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Version string `json:"version"`
}

// Required are the tools ingest and jobs need. heif-dec (libheif) decodes
// HEIC for derivatives (D26), since ffmpeg 6.1 can't.
var Required = []string{"ffmpeg", "ffprobe", "exiftool", "qpdf", "heif-dec"}

// ErrWindowsBinary means the tool resolved to a Windows install under /mnt/.
// WSL appends the Windows PATH, and those binaries see different files,
// config and credentials than Linux ones.
var ErrWindowsBinary = errors.New("resolves to a Windows binary under /mnt/; install the Linux one")

type spec struct {
	args []string
	re   *regexp.Regexp
}

var specs = map[string]spec{
	"ffmpeg":   {[]string{"-version"}, regexp.MustCompile(`(?m)^ffmpeg version (\S+)`)},
	"ffprobe":  {[]string{"-version"}, regexp.MustCompile(`(?m)^ffprobe version (\S+)`)},
	"exiftool": {[]string{"-ver"}, regexp.MustCompile(`^\s*(\d+\.\d+)\s*$`)},
	"qpdf":     {[]string{"--version"}, regexp.MustCompile(`(?m)^qpdf version (\S+)`)},
	"heif-dec": {[]string{"--version"}, regexp.MustCompile(`(?m)^(?:heif-dec\s+(?:version\s+)?)?v?(\d+\.\d+\.\d+)\b`)},
}

// ParseVersion extracts the version from a tool's version output.
func ParseVersion(name string, out []byte) (string, error) {
	s, ok := specs[name]
	if !ok {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	m := s.re.FindSubmatch(out)
	if m == nil {
		first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
		return "", fmt.Errorf("%s: unrecognized version output %q", name, first)
	}
	return string(m[1]), nil
}

// Finder resolves tools. The zero value uses the real PATH and exec.
type Finder struct {
	LookPath func(string) (string, error)
	Run      func(ctx context.Context, path string, args ...string) ([]byte, error)
}

// Lookup resolves name with the real PATH and exec.
func Lookup(ctx context.Context, name string) (Tool, error) {
	return Finder{}.Lookup(ctx, name)
}

// Lookup resolves name on PATH and asks it for its version.
func (f Finder) Lookup(ctx context.Context, name string) (Tool, error) {
	s, ok := specs[name]
	if !ok {
		return Tool{}, fmt.Errorf("unknown tool %q", name)
	}
	lookPath, run := f.LookPath, f.Run
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if run == nil {
		run = runCommand
	}

	p, err := lookPath(name)
	if err != nil {
		return Tool{Name: name}, fmt.Errorf("%s: not found on PATH", name)
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	t := Tool{Name: name, Path: p}
	if strings.HasPrefix(p, "/mnt/") {
		return t, fmt.Errorf("%s at %s: %w", name, p, ErrWindowsBinary)
	}
	out, err := run(ctx, p, s.args...)
	if err != nil {
		return t, fmt.Errorf("%s: run %s: %w", name, strings.Join(s.args, " "), err)
	}
	t.Version, err = ParseVersion(name, out)
	return t, err
}

func runCommand(ctx context.Context, path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, path, args...).Output()
}
