package sanitize

import (
	"regexp"
	"strings"
)

// The verify gate fails closed: every tag exiftool reports in a sanitized
// file must be listed here. Everything listed describes how the bytes encode
// pixels or sound (sizes, codecs, color, timing) and can't carry location,
// device or personal data. A new tag from a new camera fails ingest with its
// name instead of slipping through; add it here, with a test, only once it's
// known to be structural.
//
// Built from what exiftool 12.76 reports on the sanitized fixtures
// (testdata/media/make-media.sh), plus close structural relatives.

// allowedGroups are allowed whole: exiftool's own bookkeeping, and color
// profiles (ICC data is curves and primaries; its free-text fields name the
// profile, never the photographer).
var allowedGroups = map[string]bool{
	"ExifTool": true, "System": true, "ICC-header": true, "ICC_Profile": true,
	"ICC-meas": true, "ICC-view": true, "MPEG": true, "PNG-pHYs": true, "PNG-cHRM": true,
	"PNG-sRGB": true, "PNG-gAMA": true,
}

func set(tags ...string) map[string]bool {
	m := map[string]bool{}
	for _, t := range tags {
		m[t] = true
	}
	return m
}

// allowedTags lists structural tags per group.
var allowedTags = map[string]map[string]bool{
	// File is exiftool's view of the container. Comment (a JPEG COM segment)
	// is the one free-text field and is deliberately absent.
	"File": set("FileType", "FileTypeExtension", "MIMEType", "ImageWidth", "ImageHeight",
		"EncodingProcess", "BitsPerSample", "ColorComponents", "YCbCrSubSampling",
		"ExifByteOrder", "ID3Size", "CurrentIPTCDigest"),
	"Composite": set("ImageSize", "Megapixels", "Duration", "AvgBitrate", "Rotation"),
	"IFD0": set("Orientation", "ResolutionUnit", "XResolution", "YResolution", "YCbCrPositioning",
		"BitsPerSample", "Compression", "ImageWidth", "ImageHeight", "PhotometricInterpretation",
		"RowsPerStrip", "SamplesPerPixel", "StripByteCounts", "StripOffsets", "SubfileType",
		"PlanarConfiguration", "TileWidth", "TileLength", "TileOffsets", "TileByteCounts",
		"SampleFormat", "ExtraSamples", "Predictor", "FillOrder", "YCbCrSubSampling",
		"YCbCrCoefficients", "ReferenceBlackWhite", "WhitePoint", "PrimaryChromaticities",
		"TransferFunction", "ColorMap"),
	"ExifIFD": set("ColorSpace", "ComponentsConfiguration", "ExifVersion", "FlashpixVersion",
		"ExifImageWidth", "ExifImageHeight", "Gamma"),
	"InteropIFD": set("InteropIndex", "InteropVersion"),
	"PNG": set("BitDepth", "ColorType", "Compression", "Filter", "ImageHeight", "ImageWidth",
		"Interlace", "ProfileName", "SRGBRendering", "Gamma", "Palette", "Transparency",
		"BackgroundColor", "SignificantBits", "AnimationFrames", "AnimationPlays",
		"WhitePointX", "WhitePointY", "RedX", "RedY", "GreenX", "GreenY", "BlueX", "BlueY"),
	"GIF": set("AnimationIterations", "BackgroundColor", "BitsPerPixel", "ColorResolutionDepth",
		"Duration", "GIFVersion", "HasColorMap", "ImageHeight", "ImageWidth", "PixelAspectRatio",
		"FrameCount", "TransparentColor", "Interlaced"),
	"RIFF": set("HorizontalScale", "VerticalScale", "ImageWidth", "ImageHeight", "VP8Version",
		"WebP_Flags", "AlphaPreprocessing", "AlphaFiltering", "AlphaCompression",
		"BackgroundColor", "AnimationLoopCount", "ImageFeatures", "VP8L_Flags",
		"AvgBytesPerSec", "BitsPerSample", "Encoding", "NumChannels", "SampleRate", "BlockAlignment"),
	"AIFF": set("NumChannels", "NumSampleFrames", "SampleRate", "SampleSize", "CompressionType",
		"CompressorName", "FormatVersionTime"),
	"FLAC": set("BitsPerSample", "BlockSizeMax", "BlockSizeMin", "Channels", "FrameSizeMax",
		"FrameSizeMin", "MD5Signature", "Padding", "SampleRate", "TotalSamples"),
	// ffmpeg's FLAC muxer always names itself here; bitexact keeps the
	// version out.
	"Vorbis": set("Vendor"),
	"QuickTime": set("CompatibleBrands", "CreateDate", "CurrentTime", "Duration", "Free",
		"HandlerType", "HandlerVendorID", "MajorBrand", "MatrixStructure", "MediaData",
		"MediaDataOffset", "MediaDataSize", "MinorVersion", "ModifyDate", "MovieHeaderVersion",
		"NextTrackID", "PosterTime", "PreferredRate", "PreferredVolume", "PreviewDuration",
		"PreviewTime", "SelectionDuration", "SelectionTime", "TimeScale", "Wide", "Skip",
		// HEIF item structure
		"PrimaryItemReference", "MetaImageSize", "ImageSpatialExtent", "HEVCConfigurationVersion",
		"HEVCConfiguration", "ItemProtectionInfo", "ImagePixelDepth", "AuxiliaryImageType",
		"ColorProfiles", "ColorPrimaries", "TransferCharacteristics", "MatrixCoefficients",
		"VideoFullRangeFlag", "Rotation", "ImageRotation", "ImageMirror", "MediaDataOffset",
		"GeneralProfileSpace", "GeneralTierFlag", "GeneralProfileIDC", "GenProfileCompatibilityFlags",
		"ConstraintIndicatorFlags", "GeneralLevelIDC", "MinSpatialSegmentationIDC",
		"ParallelismType", "ChromaFormat", "BitDepthLuma", "BitDepthChroma",
		"AverageFrameRate", "ConstantFrameRate", "NumTemporalLayers", "TemporalIDNested",
		"AV1ConfigurationVersion", "AV1Configuration", "ImageGridDimensions"),
	"PDF": set("Linearized", "PDFVersion", "PageCount"),
}

// trackTags are structural tags inside QuickTime tracks (Track1, Track2, ...).
var trackTags = set("AVCConfiguration", "HEVCConfiguration", "AV1Configuration", "BitDepth",
	"ChunkOffset", "ChunkOffset64", "CompressorID", "CompressorName", "GraphicsMode",
	"HandlerClass", "HandlerDescription", "HandlerType", "ImageHeight", "ImageWidth",
	"MatrixStructure", "MediaCreateDate", "MediaDuration", "MediaHeaderVersion",
	"MediaLanguageCode", "MediaModifyDate", "MediaTimeScale", "OpColor", "PixelAspectRatio",
	"SampleSizes", "SampleToChunk", "SourceImageHeight", "SourceImageWidth", "SyncSampleTable",
	"TrackCreateDate", "TrackDuration", "TrackHeaderVersion", "TrackID", "TrackLayer",
	"TrackModifyDate", "TrackVolume", "Unknown_edts", "Unknown_esds", "Unknown_mp4a",
	"VendorID", "VideoFrameRate", "XResolution", "YResolution", "AudioBitsPerSample",
	"AudioChannels", "AudioFormat", "AudioSampleRate", "Balance", "SampleGroupDescription",
	"SampleToGroup", "TimeToSampleTable", "CompositionTimeToSample", "LayoutFlags",
	"PurchaseFileFormat", "AverageBitrate", "MaxBitrate", "BufferSize", "ColorRepresentation",
	"ColorPrimaries", "TransferCharacteristics", "MatrixCoefficients", "VideoFullRangeFlag",
	"CleanApertureDimensions", "ProductionApertureDimensions", "EncodedPixelsDimensions",
	"FieldInfo", "GammaLevel", "SampleDescriptions", "EditList", "AudioProfile",
	"AudioVendorID", "VideoProfile", "Unknown_btrt", "Unknown_pasp", "Unknown_colr",
	"Unknown_sgpd", "Unknown_sbgp", "Unknown_stps", "Unknown_ctts", "Unknown_avc1",
	"Unknown_hvc1", "Unknown_dOps", "Unknown_alac", "Unknown_chan")

var trackGroup = regexp.MustCompile(`^Track[0-9]+$`)

// denied names the obviously private tags, only so the failure message says
// what kind of thing survived. The allowlist alone decides pass or fail.
var denied = regexp.MustCompile(`(?i)gps|location|iso6709|serial|owner|artist|author|creator|by-?line|` +
	`\bmake\b|^make$|^model$|lensmodel|camera|uniqueid|documentid|instanceid|comment|description|` +
	`title|keywords|city|country|province|state$|sublocation|address|copyright|software|host` +
	`computer|datetimeoriginal|^createdate$|subject|person|face|region|contact|email|phone`)

// allowed reports whether exiftool's "Group:Tag" key is structural.
func allowed(key string) bool {
	group, tag, ok := strings.Cut(key, ":")
	if !ok {
		return key == "SourceFile"
	}
	if allowedGroups[group] {
		return true
	}
	if trackGroup.MatchString(group) {
		return trackTags[tag]
	}
	return allowedTags[group][tag]
}

func reason(key string) string {
	if denied.MatchString(key) {
		return "personal, device or location data survived"
	}
	return "not on the structural allowlist"
}
