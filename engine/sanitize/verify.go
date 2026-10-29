package sanitize

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/nick5616/canon/engine/media"
)

// Verified is a file that passed the verify gate. Only Verify makes one;
// the blob store only accepts one.
type Verified struct {
	path string
	sum  string
	size int64
	typ  media.Type
	tags map[string]any
}

func (v Verified) Path() string         { return v.path }
func (v Verified) SHA256() string       { return v.sum }
func (v Verified) Size() int64          { return v.size }
func (v Verified) Type() media.Type     { return v.typ }
func (v Verified) Tags() map[string]any { return v.tags }

// RejectedError is a hard stop: something private survived.
type RejectedError struct {
	Findings []Finding
}

func (e *RejectedError) Error() string {
	var parts []string
	for i, f := range e.Findings {
		if i == 5 {
			parts = append(parts, fmt.Sprintf("and %d more", len(e.Findings)-5))
			break
		}
		parts = append(parts, f.Tag+" ("+f.Reason+")")
	}
	return "verify failed: " + strings.Join(parts, "; ")
}

// Verifier checks sanitized output.
type Verifier struct {
	Run Runner
}

// NewVerifier uses the real tools.
func NewVerifier() Verifier { return Verifier{Run: NewTools().Run} }

// Verify re-reads path from scratch and fails if anything private remains.
// On success the file is hashed and returned as Verified.
func (v Verifier) Verify(ctx context.Context, t media.Type, path string) (Verified, error) {
	// The file must still be the type it was sanitized as.
	f, err := os.Open(path)
	if err != nil {
		return Verified{}, err
	}
	got, err := media.Detect(f)
	f.Close()
	if err != nil {
		return Verified{}, fmt.Errorf("verify: %w", err)
	}
	if got.Name != t.Name {
		return Verified{}, fmt.Errorf("verify: sanitized output is %s, expected %s", got.Name, t.Name)
	}

	tags, err := v.exif(ctx, path)
	if err != nil {
		return Verified{}, err
	}
	var findings []Finding
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !allowed(k) {
			findings = append(findings, Finding{Tag: k, Value: short(tags[k]), Reason: reason(k)})
		}
	}

	var extra []Finding
	switch t.Family {
	case media.FamilyPNG:
		extra, err = pngFindings(path)
	case media.FamilyAV:
		extra, err = v.streams(ctx, t, path)
	case media.FamilyPDF:
		extra, err = v.pdfObjects(ctx, path)
	}
	if err != nil {
		return Verified{}, err
	}
	// Structural findings first: they say what to do about it.
	findings = append(extra, findings...)
	if len(findings) > 0 {
		return Verified{}, &RejectedError{Findings: findings}
	}

	sum, size, err := hashFile(path)
	if err != nil {
		return Verified{}, err
	}
	return Verified{path: path, sum: sum, size: size, typ: t, tags: tags}, nil
}

// exif reads every tag exiftool can find, including unknown ones (-u),
// duplicates (-a) and embedded streams such as timed metadata (-ee).
func (v Verifier) exif(ctx context.Context, path string) (map[string]any, error) {
	out, err := v.Run(ctx, "exiftool", "-config", "", "-j", "-G1", "-a", "-u", "-ee", "-n", path)
	if err != nil {
		return nil, err
	}
	var res []map[string]any
	if err := json.Unmarshal(out, &res); err != nil || len(res) != 1 {
		return nil, fmt.Errorf("verify: unexpected exiftool output")
	}
	tags := res[0]
	delete(tags, "SourceFile")
	if w, ok := tags["ExifTool:Error"]; ok {
		return nil, fmt.Errorf("verify: exiftool: %v", w)
	}
	return tags, nil
}

// streams checks that only audio and video remain (no data, subtitle or
// timed-metadata tracks, no cover art) and that no container or stream tags
// survived beyond ffmpeg's structural ones.
func (v Verifier) streams(ctx context.Context, t media.Type, path string) ([]Finding, error) {
	out, err := v.Run(ctx, "ffprobe", "-v", "error", "-protocol_whitelist", "file,pipe",
		"-f", t.Demuxer, "-show_streams", "-show_format", "-show_chapters", "-of", "json", path)
	if err != nil {
		return nil, err
	}
	var probe struct {
		Streams []struct {
			Index       int               `json:"index"`
			CodecType   string            `json:"codec_type"`
			Disposition map[string]int    `json:"disposition"`
			Tags        map[string]string `json:"tags"`
		} `json:"streams"`
		Chapters []any `json:"chapters"`
		Format   struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		return nil, fmt.Errorf("verify: ffprobe output: %w", err)
	}
	var fs []Finding
	okTag := set("major_brand", "minor_version", "compatible_brands", "handler_name", "vendor_id", "language", "encoder")
	for k, val := range probe.Format.Tags {
		if !okTag[k] || (k == "encoder" && !strings.HasPrefix(val, "Lav")) {
			fs = append(fs, Finding{Tag: "container tag " + k, Value: val, Reason: reason(k)})
		}
	}
	for _, s := range probe.Streams {
		name := fmt.Sprintf("stream %d (%s)", s.Index, s.CodecType)
		switch {
		case s.CodecType != "video" && s.CodecType != "audio":
			fs = append(fs, Finding{Tag: name, Reason: "only audio and video streams may remain"})
		case s.CodecType == "video" && (t.Kind != media.Video || s.Disposition["attached_pic"] == 1):
			fs = append(fs, Finding{Tag: name, Reason: "cover art or a picture stream remained"})
		}
		for k, val := range s.Tags {
			if !okTag[k] {
				fs = append(fs, Finding{Tag: name + " tag " + k, Value: val, Reason: reason(k)})
			}
		}
	}
	if len(probe.Chapters) > 0 {
		fs = append(fs, Finding{Tag: "chapters", Reason: "chapters can carry titles and notes"})
	}
	return fs, nil
}

var pdfObjKey = regexp.MustCompile(`^obj:(\d+) (\d+) R$`)

// pdfObjects fails on attached files and on embedded images that carry
// their own metadata (a phone scan saved as PDF keeps the photo's EXIF,
// which neither exiftool nor qpdf touches).
func (v Verifier) pdfObjects(ctx context.Context, path string) ([]Finding, error) {
	out, err := v.Run(ctx, "qpdf", "--json=2", "--json-key=qpdf", path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		QPDF []json.RawMessage `json:"qpdf"`
	}
	if err := json.Unmarshal(out, &doc); err != nil || len(doc.QPDF) < 2 {
		return nil, fmt.Errorf("verify: qpdf json: %v", err)
	}
	var objects map[string]struct {
		Value  any `json:"value"`
		Stream *struct {
			Dict map[string]any `json:"dict"`
		} `json:"stream"`
	}
	if err := json.Unmarshal(doc.QPDF[1], &objects); err != nil {
		return nil, fmt.Errorf("verify: qpdf objects: %w", err)
	}
	var fs []Finding
	keys := make([]string, 0, len(objects))
	for k := range objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		o := objects[key]
		m := pdfObjKey.FindStringSubmatch(key)
		if m == nil {
			continue
		}
		if dict, ok := o.Value.(map[string]any); ok {
			if dict["/EmbeddedFiles"] != nil || dict["/Type"] == "/EmbeddedFile" || dict["/Type"] == "/Filespec" {
				fs = append(fs, Finding{Tag: "PDF object " + m[1], Reason: "attached files aren't allowed; remove the attachment and re-export"})
			}
		}
		if o.Stream == nil {
			continue
		}
		d := o.Stream.Dict
		if d["/Type"] == "/EmbeddedFile" {
			fs = append(fs, Finding{Tag: "PDF object " + m[1], Reason: "attached files aren't allowed; remove the attachment and re-export"})
			continue
		}
		if d["/Subtype"] != "/Image" {
			continue
		}
		filter := fmt.Sprint(d["/Filter"])
		if !strings.Contains(filter, "DCTDecode") && !strings.Contains(filter, "JPXDecode") {
			continue // raw pixel streams can't carry EXIF
		}
		raw, err := v.Run(ctx, "qpdf", "--show-object="+m[1]+","+m[2], "--raw-stream-data", path)
		if err != nil {
			return nil, err
		}
		if what := imageMetadata(raw); what != "" {
			fs = append(fs, Finding{
				Tag:    "PDF image object " + m[1],
				Reason: "embedded image carries " + what + "; re-export the PDF without metadata (or from the scan app's 'flatten' option)",
			})
		}
	}
	return fs, nil
}

// imageMetadata names any metadata block in a raw JPEG or JPEG 2000 stream.
func imageMetadata(b []byte) string {
	switch {
	case bytes.Contains(b, []byte("Exif\x00\x00")):
		return "EXIF"
	case bytes.Contains(b, []byte("http://ns.adobe.com/xap/1.0/")), bytes.Contains(b, []byte("<x:xmpmeta")):
		return "XMP"
	case bytes.Contains(b, []byte("Photoshop 3.0\x00")):
		return "IPTC"
	}
	return ""
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func short(v any) string {
	s := fmt.Sprint(v)
	if len(s) > 80 {
		s = s[:77] + "..."
	}
	return s
}
