package sanitize

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nick5616/canon/engine/media"
	"github.com/nick5616/canon/internal/testmedia"
)

// fixtures returns every fixture that must ingest clean.
func fixtures(t *testing.T) []string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(testmedia.Dir(t), "*.*"))
	if len(files) < 14 {
		t.Fatalf("only %d fixtures", len(files))
	}
	return files
}

func detect(t *testing.T, path string) media.Type {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	typ, err := media.Detect(f)
	if err != nil {
		t.Fatal(err)
	}
	return typ
}

func sanitize(t *testing.T, s Sanitizer, in string) (string, media.Type) {
	t.Helper()
	typ := detect(t, in)
	out := filepath.Join(t.TempDir(), "out."+typ.Ext)
	if err := s.Sanitize(context.Background(), typ, in, out); err != nil {
		t.Fatalf("%s: sanitize: %v", filepath.Base(in), err)
	}
	return out, typ
}

// readable returns the file's bytes plus every zlib stream inside it
// decompressed (PNG text chunks, PDF streams), so a planted string can't
// hide in compressed data.
func readable(t *testing.T, path string, typ media.Type) []byte {
	t.Helper()
	b, _ := os.ReadFile(path)
	all := append([]byte{}, b...)
	for i := 0; i+2 < len(b); i++ {
		if b[i] == 0x78 && (b[i+1] == 0x9c || b[i+1] == 0x01 || b[i+1] == 0xda || b[i+1] == 0x5e) {
			if r, err := zlib.NewReader(bytes.NewReader(b[i:])); err == nil {
				d, _ := io.ReadAll(io.LimitReader(r, 1<<20))
				all = append(all, d...)
			}
		}
	}
	if typ.Family == media.FamilyPDF {
		qdf := filepath.Join(t.TempDir(), "out.qdf")
		if out, err := exec.Command("qpdf", "--qdf", "--object-streams=disable", path, qdf).CombinedOutput(); err != nil {
			t.Fatalf("qpdf --qdf: %v %s", err, out)
		}
		d, _ := os.ReadFile(qdf)
		all = append(all, d...)
	}
	return all
}

// The doc's privacy test: nothing planted survives, by exiftool or by bytes.
func TestNothingPrivateSurvives(t *testing.T) {
	s, v := NewTools(), NewVerifier()
	for _, in := range fixtures(t) {
		t.Run(filepath.Base(in), func(t *testing.T) {
			out, typ := sanitize(t, s, in)
			if _, err := v.Verify(context.Background(), typ, out); err != nil {
				t.Fatalf("verify: %v", err)
			}
			body := readable(t, out, typ)
			for _, p := range testmedia.Planted {
				if bytes.Contains(body, []byte(p)) {
					t.Errorf("planted %q survived in the bytes", p)
				}
			}
			// Control: the original really did carry them.
			if orig := readable(t, in, typ); !bytes.Contains(orig, []byte("Fake")) && !bytes.Contains(orig, []byte("SN-FAKE")) {
				t.Logf("note: %s has no plain-text plant (GPS is binary)", filepath.Base(in))
			}
		})
	}
}

// passThrough "sanitizes" by copying: a broken sanitizer.
type passThrough struct{}

func (passThrough) Sanitize(_ context.Context, _ media.Type, in, out string) error {
	b, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	return os.WriteFile(out, b, 0o600)
}

// The gate is a hard stop: with a sanitizer that does nothing, every
// fixture is rejected.
func TestGateStopsBrokenSanitizer(t *testing.T) {
	v := NewVerifier()
	for _, in := range fixtures(t) {
		t.Run(filepath.Base(in), func(t *testing.T) {
			out, typ := sanitize(t, passThrough{}, in)
			_, err := v.Verify(context.Background(), typ, out)
			var re *RejectedError
			if !errors.As(err, &re) {
				t.Fatalf("want rejection, got %v", err)
			}
			if len(re.Findings) == 0 || re.Error() == "" {
				t.Fatal("rejection without findings")
			}
		})
	}
}

func TestFailsClosedOnUnknownTag(t *testing.T) {
	out, typ := sanitize(t, NewTools(), filepath.Join(testmedia.Dir(t), "jpeg.jpg"))
	// A harmless-sounding tag no denylist would catch.
	if b, err := exec.Command("exiftool", "-config", "", "-q", "-overwrite_original", "-XMP-xmp:Label=blue", out).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	_, err := NewVerifier().Verify(context.Background(), typ, out)
	var re *RejectedError
	if !errors.As(err, &re) || !strings.Contains(re.Error(), "XMP-xmp:Label") || !strings.Contains(re.Error(), "not on the structural allowlist") {
		t.Fatalf("got %v", err)
	}
}

func TestPNGChunks(t *testing.T) {
	in := filepath.Join(testmedia.Dir(t), "png.png")
	findings, err := pngFindings(in)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range findings {
		names = append(names, f.Tag)
	}
	got := strings.Join(names, " ")
	for _, want := range []string{"zTXt", "tIME"} {
		if !strings.Contains(got, want) {
			t.Errorf("original's %s chunk not flagged: %s", want, got)
		}
	}
	out, _ := sanitize(t, NewTools(), in)
	if f, _ := pngFindings(out); len(f) != 0 {
		t.Errorf("sanitized PNG still has %v", f)
	}
}

func TestPDFWithGPSImageIsRejected(t *testing.T) {
	in := filepath.Join(testmedia.Dir(t), "hostile", "pdf-embedded-gps.pdf")
	out, typ := sanitize(t, NewTools(), in)
	_, err := NewVerifier().Verify(context.Background(), typ, out)
	var re *RejectedError
	if !errors.As(err, &re) || !strings.Contains(re.Error(), "embedded image carries EXIF") || !strings.Contains(re.Error(), "re-export") {
		t.Fatalf("got %v", err)
	}
}

func TestDeterministic(t *testing.T) {
	s := NewTools()
	for _, in := range fixtures(t) {
		a, _ := sanitize(t, s, in)
		b, _ := sanitize(t, s, in)
		ab, _ := os.ReadFile(a)
		bb, _ := os.ReadFile(b)
		if !bytes.Equal(ab, bb) {
			t.Errorf("%s: two runs differ", filepath.Base(in))
		}
	}
}

// What must survive: orientation, color profiles, and every audio and
// video packet.
func TestPreserved(t *testing.T) {
	dir := testmedia.Dir(t)
	s := NewTools()

	jpg, _ := sanitize(t, s, filepath.Join(dir, "jpeg.jpg"))
	out, _ := exec.Command("exiftool", "-config", "", "-s3", "-n", "-Orientation", "-ICC_Profile:ProfileCopyright", jpg).Output()
	if got := strings.Fields(string(out)); len(got) < 2 || got[0] != "6" {
		t.Errorf("jpeg orientation/ICC: %q", out)
	}
	png, _ := sanitize(t, s, filepath.Join(dir, "png.png"))
	if out, _ := exec.Command("exiftool", "-config", "", "-s3", "-ICC_Profile:ProfileCopyright", png).Output(); !bytes.Contains(out, []byte("Canon test profile")) {
		t.Errorf("png lost its iCCP profile")
	}

	for _, name := range []string{"mov.mov", "mp4.mp4", "mp3.mp3", "aiff.aiff", "wav.wav", "flac.flac", "m4a.m4a"} {
		in := filepath.Join(dir, name)
		out, typ := sanitize(t, s, in)
		if a, b := framemd5(t, typ, in, false), framemd5(t, typ, out, true); a != b {
			t.Errorf("%s: packets changed\n%s\nvs\n%s", name, a, b)
		}
	}
}

// framemd5 hashes every audio/video packet. For the original, only the
// streams the sanitizer keeps are hashed.
func framemd5(t *testing.T, typ media.Type, path string, sanitized bool) string {
	t.Helper()
	maps := []string{"-map", "0:a"}
	if typ.Kind == media.Video {
		maps = []string{"-map", "0:V", "-map", "0:a?"}
	}
	args := append([]string{"-v", "error", "-f", typ.Demuxer, "-i", path}, maps...)
	args = append(args, "-c", "copy", "-f", "framemd5", "-")
	out, err := exec.Command("ffmpeg", args...).Output()
	if err != nil {
		t.Fatalf("framemd5 %s: %v", path, err)
	}
	// Columns: stream, dts, pts, duration, size, hash. Timestamps are left
	// out: MP3 encoder-delay handling shifts them by a constant tick, while
	// the packets themselves are identical.
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		f := strings.Split(l, ",")
		lines = append(lines, strings.Join(append([]string{f[0]}, f[3:]...), ","))
	}
	return strings.Join(lines, "\n")
}

func TestHEIC(t *testing.T) {
	testmedia.Need(t, "heif-enc", "libheif-examples")
	in := filepath.Join(testmedia.Dir(t), "heic.heic")
	out, typ := sanitize(t, NewTools(), in)
	if typ != media.HEIC {
		t.Fatalf("detected %s", typ.Name)
	}
	if _, err := NewVerifier().Verify(context.Background(), typ, out); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestWrongExtension(t *testing.T) {
	out, typ := sanitize(t, NewTools(), filepath.Join(testmedia.Dir(t), "jpeg-named.HEIC"))
	if typ != media.JPEG {
		t.Fatalf("detected %s", typ.Name)
	}
	if _, err := NewVerifier().Verify(context.Background(), typ, out); err != nil {
		t.Fatal(err)
	}
}

// ffmpeg is only ever pointed at local files.
func TestFFmpegArgs(t *testing.T) {
	var calls [][]string
	rec := Tools{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return nil, nil
	}}
	for _, typ := range []media.Type{media.MOV, media.MP4, media.MP3, media.M4A, media.AIFF, media.WAV, media.FLAC} {
		calls = nil
		_ = rec.Sanitize(context.Background(), typ, "/x/in."+typ.Ext, filepath.Join(t.TempDir(), "o."+typ.Ext))
		cmd := strings.Join(calls[0], " ")
		for _, want := range []string{"-protocol_whitelist file,pipe", "-f " + typ.Demuxer + " -i", "-map_metadata -1", "-c copy", "+bitexact"} {
			if !strings.Contains(cmd, want) {
				t.Errorf("%s: missing %q in %s", typ.Name, want, cmd)
			}
		}
		if strings.Contains(cmd, "-map 0 ") || strings.HasSuffix(cmd, "-map 0") {
			t.Errorf("%s: maps every stream: %s", typ.Name, cmd)
		}
	}
}
