#!/usr/bin/env bash
# Builds synthetic media fixtures for the ingest tests. Nothing real is ever
# committed: every file here is generated, and every one carries the same fake
# private data in every place its format can hold it:
#
#   GPS 37.7749, -122.4194   serial SN-FAKE-0042   owner "Fake Owner"
#   make FakeCam / model X1  capture date 2024-05-06 07:08:09
#
#   testdata/media/make-media.sh [OUT_DIR]     (default testdata/_out/media)
#
# Files under hostile/ must be rejected; everything else must ingest clean.
# Needs ffmpeg, exiftool, qpdf, python3; heif-enc (libheif-examples) for HEIC.
set -euo pipefail

OUT=${1:-"$(cd "$(dirname "$0")/../.." && pwd)/testdata/_out/media"}
rm -rf "$OUT"
mkdir -p "$OUT/hostile"
cd "$OUT"

ff() { ffmpeg -nostdin -hide_banner -loglevel error -y "$@"; }
et() { exiftool -config "" -q -q -overwrite_original "$@"; }

GPS=(-GPSLatitude=37.7749 -GPSLatitudeRef=N -GPSLongitude=122.4194 -GPSLongitudeRef=W -GPSAltitude=12 -GPSAltitudeRef=0)
WHO=(-SerialNumber=SN-FAKE-0042 -OwnerName="Fake Owner" -Artist="Fake Owner" -Make=FakeCam -Model=X1
  -Software=FakeSoft -DateTimeOriginal="2024:05:06 07:08:09" -ImageUniqueID=0123456789abcdef)
XMP=(-XMP-dc:Creator="Fake Owner" -XMP-exif:GPSLatitude="37.7749 N" -XMP-exif:GPSLongitude="122.4194 W"
  -XMP-photoshop:City=FakeCity -XMP-aux:SerialNumber=SN-FAKE-0042)

# A minimal ICC v2 profile (header plus one copyright tag), so tests can
# check that color profiles survive sanitizing.
python3 - <<'EOF'
import struct
text = b"text\0\0\0\0Canon test profile\0"
text += b"\0" * (-len(text) % 4)
size = 128 + 4 + 12 + len(text)
hdr = bytearray(128)
struct.pack_into(">I", hdr, 0, size)
hdr[4:8] = b"none"; hdr[8:12] = bytes([2, 0x10, 0, 0]); hdr[12:16] = b"mntr"
hdr[16:20] = b"RGB "; hdr[20:24] = b"XYZ "; hdr[36:40] = b"acsp"
struct.pack_into(">iii", hdr, 68, 63190, 65536, 54061)  # D50 illuminant
table = struct.pack(">I4sII", 1, b"cprt", 128 + 4 + 12, len(text))
open("profile.icc", "wb").write(bytes(hdr) + table + text)
EOF

# --- Images -----------------------------------------------------------------
ff -f lavfi -i testsrc=size=320x240 -frames:v 1 jpeg.jpg
et "${GPS[@]}" "${WHO[@]}" "${XMP[@]}" -Orientation#=6 -IPTC:By-line="Fake Owner" -IPTC:City=FakeCity \
  -Comment="shot at 37.7749 by Fake Owner" "-ICC_Profile<=profile.icc" jpeg.jpg

# The artandmedia case: a JPEG with a .HEIC name.
cp jpeg.jpg jpeg-named.HEIC

ff -f lavfi -i testsrc=size=64x48 -frames:v 1 png.png
et "${GPS[@]}" -SerialNumber=SN-FAKE-0042 -XMP-dc:Creator="Fake Owner" -PNG:Author="Fake Owner" \
  -PNG:Comment="SN-FAKE-0042" "-ICC_Profile<=profile.icc" png.png
# The legacy zTXt raw profile exiftool can't strip (found in artandmedia).
python3 - <<'EOF'
import struct, zlib
def chunk(t, b): return struct.pack(">I", len(b)) + t + b + struct.pack(">I", zlib.crc32(t + b) & 0xffffffff)
d = open("png.png", "rb").read()
raw = b"\nexif\n  40\nGPSLatitude 37.7749 SerialNumber SN-FAKE-0042 Fake Owner\n"
z = chunk(b"zTXt", b"Raw profile type exif\x00\x00" + zlib.compress(raw))
i = d.index(b"IDAT") - 4
open("png.png", "wb").write(d[:i] + z + chunk(b"tIME", b"\x07\xe8\x05\x06\x07\x08\x09") + d[i:])
EOF

ff -f lavfi -i testsrc=size=64x48 -frames:v 1 webp.webp
et "${GPS[@]}" "${WHO[@]}" "${XMP[@]}" webp.webp

ff -f lavfi -i testsrc=size=64x48 -frames:v 1 -pix_fmt rgb24 tiff.tif
et "${GPS[@]}" "${WHO[@]}" "${XMP[@]}" -ImageDescription="Fake Owner at home" tiff.tif

ff -f lavfi -i testsrc=size=64x48 -frames:v 1 gif.gif
et -XMP-dc:Creator="Fake Owner" -XMP-exif:GPSLatitude="37.7749 N" -Comment="SN-FAKE-0042" gif.gif

if command -v heif-enc >/dev/null; then
  ff -f lavfi -i testsrc=size=128x96 -frames:v 1 heic-src.png
  heif-enc -q 50 -o heic.heic heic-src.png >/dev/null
  rm heic-src.png
  et "${GPS[@]}" "${WHO[@]}" "${XMP[@]}" heic.heic
else
  echo "make-media: heif-enc not found; skipping HEIC (install libheif-examples)" >&2
fi

# --- Video ------------------------------------------------------------------
printf '1\n00:00:00,000 --> 00:00:01,000\nGPS 37.7749 -122.4194 SN-FAKE-0042\n' >subs.srt
ff -f lavfi -i testsrc=size=160x120:rate=10 -f lavfi -i sine=d=1 -i subs.srt -t 1 \
  -map 0 -map 1 -map 2 -c:v libx264 -preset ultrafast -c:a aac -c:s mov_text \
  -metadata "com.apple.quicktime.location.ISO6709=+37.7749-122.4194/" \
  -metadata "com.apple.quicktime.make=FakeCam" -metadata "com.apple.quicktime.model=X1" \
  -metadata "com.apple.quicktime.creationdate=2024-05-06T07:08:09Z" \
  -metadata artist="Fake Owner" -metadata comment="SN-FAKE-0042" -movflags use_metadata_tags mov.mov
et -UserData:GPSCoordinates="37.7749, -122.4194" -UserData:SerialNumber=SN-FAKE-0042 mov.mov
rm subs.srt

ff -f lavfi -i testsrc=size=160x120:rate=10 -f lavfi -i sine=d=1 -i jpeg.jpg -t 1 \
  -map 0 -map 1 -map 2 -c:v:0 libx264 -preset ultrafast -c:a aac -c:v:1 mjpeg -disposition:v:1 attached_pic \
  -metadata artist="Fake Owner" -metadata location="+37.7749-122.4194/" -metadata comment="SN-FAKE-0042" mp4.mp4

# --- Audio ------------------------------------------------------------------
ff -f lavfi -i sine=d=1 -i jpeg.jpg -map 0 -map 1 -c:a libmp3lame -c:v copy -id3v2_version 3 \
  -disposition:v attached_pic -metadata artist="Fake Owner" -metadata comment="SN-FAKE-0042 37.7749" mp3.mp3
ff -f lavfi -i sine=d=1 -c:a pcm_s16be -write_id3v2 1 -metadata artist="Fake Owner" \
  -metadata comment="GPS 37.7749 SN-FAKE-0042" aiff.aiff
ff -f lavfi -i sine=d=1 -c:a pcm_s16le -metadata artist="Fake Owner" -metadata comment="SN-FAKE-0042" wav.wav
ff -f lavfi -i sine=d=1 -i jpeg.jpg -map 0 -map 1 -c:a flac -c:v copy -disposition:v attached_pic \
  -metadata artist="Fake Owner" -metadata comment="SN-FAKE-0042 37.7749" flac.flac
ff -f lavfi -i sine=d=1 -i jpeg.jpg -map 0 -map 1 -c:a aac -c:v copy -disposition:v attached_pic \
  -metadata artist="Fake Owner" -metadata comment="SN-FAKE-0042" m4a.m4a

# --- PDF --------------------------------------------------------------------
python3 - <<'EOF'
def pdf(objs, info):
    out = b"%PDF-1.4\n"; offs = []
    for i, o in enumerate(objs, 1):
        offs.append(len(out)); out += b"%d 0 obj\n" % i + o + b"\nendobj\n"
    x = len(out)
    out += b"xref\n0 %d\n0000000000 65535 f \n" % (len(objs) + 1) + b"".join(b"%010d 00000 n \n" % o for o in offs)
    out += b"trailer\n<< /Size %d /Root 1 0 R /Info %d 0 R >>\nstartxref\n%d\n%%%%EOF\n" % (len(objs) + 1, info, x)
    return out

base = [b"<< /Type /Catalog /Pages 2 0 R >>", b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
        b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>",
        b"<< /Author (Fake Owner) /Creator (SN-FAKE-0042 scanner) /Title (GPS 37.7749) >>"]
doc = pdf(base, 4)
# An incremental update that replaces the Info dict but leaves the old one in the bytes.
off = len(doc)
upd = b"5 0 obj\n<< /Author (Someone Else) >>\nendobj\n"
x = off + len(upd)
upd += b"xref\n0 1\n0000000000 65535 f \n5 1\n%010d 00000 n \n" % off
prev = doc.rindex(b"startxref\n") + len(b"startxref\n")
prev = int(doc[prev:doc.index(b"\n", prev)])
upd += b"trailer\n<< /Size 6 /Root 1 0 R /Info 5 0 R /Prev %d >>\nstartxref\n%d\n%%%%EOF\n" % (prev, x)
open("pdf.pdf", "wb").write(doc + upd)

# A scanned page: an embedded JPEG whose own EXIF has GPS. Must be rejected.
jpg = open("jpeg.jpg", "rb").read()
img = b"<< /Type /XObject /Subtype /Image /Width 320 /Height 240 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode /Length %d >>\nstream\n" % len(jpg) + jpg + b"\nendstream"
page = b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 320 240] /Resources << /XObject << /Im0 5 0 R >> >> /Contents 6 0 R >>"
content = b"q 320 0 0 240 0 0 cm /Im0 Do Q"
objs = base[:2] + [page, b"<< /Title (scan) >>", img, b"<< /Length %d >>\nstream\n" % len(content) + content + b"\nendstream"]
open("hostile/pdf-embedded-gps.pdf", "wb").write(pdf(objs, 4))
EOF

# --- Hostile: must be rejected ------------------------------------------------
printf '#EXTM3U\n#EXT-X-VERSION:3\n#EXTINF:1,\nfile:///etc/passwd\n' >hostile/playlist.mp4
printf 'ffconcat version 1.0\nfile /etc/passwd\n' >hostile/concat.mov
printf 'PK\003\004\024\000\000\000' >hostile/archive.jpg
head -c 600 jpeg.jpg >hostile/truncated.jpg
: >hostile/empty.png

rm -f profile.icc
echo "make-media: wrote $(find . -type f | wc -l) files to $OUT"
