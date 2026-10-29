package sanitize

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
)

var pngSig = []byte("\x89PNG\r\n\x1a\n")

// pngKeep are the chunks that describe pixels and color, and nothing else.
// Text (tEXt, zTXt, iTXt), eXIf, tIME and every private chunk are dropped.
// exiftool can't be used here: it leaves zTXt raw profiles it can't parse,
// GPS included (found in the artandmedia bucket).
var pngKeep = map[string]bool{
	"IHDR": true, "PLTE": true, "IDAT": true, "IEND": true,
	"tRNS": true, "cHRM": true, "gAMA": true, "iCCP": true, "sBIT": true,
	"sRGB": true, "pHYs": true, "bKGD": true, "cICP": true,
	"acTL": true, "fcTL": true, "fdAT": true, // APNG frames
}

type pngChunk struct {
	typ  string
	data []byte
}

// readPNG splits a PNG into chunks, checking the signature and every CRC.
func readPNG(b []byte) ([]pngChunk, error) {
	if !bytes.HasPrefix(b, pngSig) {
		return nil, errors.New("png: bad signature")
	}
	b = b[len(pngSig):]
	var out []pngChunk
	for len(b) > 0 {
		if len(b) < 12 {
			return nil, errors.New("png: truncated chunk")
		}
		n := binary.BigEndian.Uint32(b[0:4])
		if uint64(n)+12 > uint64(len(b)) {
			return nil, errors.New("png: chunk runs past end of file")
		}
		typ, data := string(b[4:8]), b[8:8+n]
		if crc32.ChecksumIEEE(b[4:8+n]) != binary.BigEndian.Uint32(b[8+n:12+n]) {
			return nil, fmt.Errorf("png: bad CRC in %q chunk", typ)
		}
		out = append(out, pngChunk{typ, data})
		b = b[12+n:]
		if typ == "IEND" {
			break // anything after IEND is dropped
		}
	}
	if len(out) == 0 || out[0].typ != "IHDR" || out[len(out)-1].typ != "IEND" {
		return nil, errors.New("png: missing IHDR or IEND")
	}
	return out, nil
}

func sanitizePNG(in, out string) error {
	b, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	chunks, err := readPNG(b)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.Write(pngSig)
	for _, c := range chunks {
		if !pngKeep[c.typ] {
			continue
		}
		var hdr [8]byte
		binary.BigEndian.PutUint32(hdr[0:4], uint32(len(c.data)))
		copy(hdr[4:], c.typ)
		buf.Write(hdr[:])
		buf.Write(c.data)
		binary.Write(&buf, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(c.typ), c.data...)))
	}
	return os.WriteFile(out, buf.Bytes(), 0o600)
}

// pngFindings reports any chunk outside the allowlist, so verify catches a
// PNG that didn't go through sanitizePNG.
func pngFindings(path string) ([]Finding, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	chunks, err := readPNG(b)
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, c := range chunks {
		if !pngKeep[c.typ] {
			out = append(out, Finding{Tag: "PNG:" + c.typ + " chunk", Reason: "PNG chunk outside the allowlist (text, EXIF or private data)"})
		}
	}
	return out, nil
}
