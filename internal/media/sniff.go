package media

import (
	"bytes"
	"net/http"
)

// SniffLen is how many leading bytes Sniff inspects. http.DetectContentType
// only looks at 512 bytes; the rest lets track metadata of audio/video
// containers be examined.
const SniffLen = 64 << 10

// Sniff returns the sniffed content type and attachment kind for the first
// bytes (up to SniffLen) of a file.
func Sniff(head []byte) (contentType, kind string) {
	if len(head) > SniffLen {
		head = head[:SniffLen]
	}
	contentType = refineContainer(http.DetectContentType(head), head)
	return contentType, detectedKind(contentType)
}

// refineContainer narrows container types that http.DetectContentType always
// reports as video (WebM, MP4) or as a generic stream (Ogg, QuickTime) when
// their track metadata shows audio only, which is what browsers'
// MediaRecorder produces for voice notes, and recognizes QuickTime movies.
// Unknown layouts keep the original type.
func refineContainer(contentType string, head []byte) string {
	switch contentType {
	case "video/webm":
		if audio, video := matroskaTracks(head); audio && !video {
			return "audio/webm"
		}
	case "video/mp4", "application/octet-stream":
		if !isFtyp(head) {
			return contentType
		}
		audio, video := mp4Tracks(head)
		switch {
		case audio && !video, !video && isAudioBrand(head[8:12]):
			return "audio/mp4"
		case string(head[8:12]) == "qt  ":
			// iPhone camera .mov files; DetectContentType only knows "mp4" brands.
			return "video/quicktime"
		}
	case "application/ogg":
		if bytes.Contains(head, []byte("\x80theora")) {
			return "video/ogg"
		}
		for _, sig := range oggAudioSigs {
			if bytes.Contains(head, sig) {
				return "audio/ogg"
			}
		}
	}
	return contentType
}

var oggAudioSigs = [][]byte{[]byte("OpusHead"), []byte("\x01vorbis"), []byte("\x7fFLAC"), []byte("Speex   ")}

// matroskaTracks scans for Matroska CodecID elements (ID 0x86 with a one-byte
// size) holding IDs such as "A_OPUS" or "V_VP8".
func matroskaTracks(head []byte) (audio, video bool) {
	for i := 0; i+2 < len(head); i++ {
		if head[i] != 0x86 || head[i+1] < 0x83 || head[i+1] == 0xff {
			continue
		}
		n := int(head[i+1] & 0x7f)
		if i+2+n > len(head) || !isCodecID(head[i+2:i+2+n]) {
			continue
		}
		switch head[i+2] {
		case 'A':
			audio = true
		case 'V':
			video = true
		}
	}
	return audio, video
}

func isCodecID(b []byte) bool {
	if len(b) < 3 || (b[0] != 'A' && b[0] != 'V') || b[1] != '_' {
		return false
	}
	for _, c := range b[2:] {
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '/' || c == '.') {
			return false
		}
	}
	return true
}

func isFtyp(head []byte) bool {
	return len(head) >= 12 && string(head[4:8]) == "ftyp"
}

func isAudioBrand(brand []byte) bool {
	switch string(brand) {
	case "M4A ", "M4B ", "M4P ", "F4A ", "F4B ":
		return true
	}
	return false
}

// mp4Tracks reads the handler type of each "hdlr" box (version/flags and
// pre_defined precede it) to find sound and video tracks. A moov box placed
// after the media data is not inspected.
func mp4Tracks(head []byte) (audio, video bool) {
	marker := []byte("hdlr")
	for off := 0; ; {
		i := bytes.Index(head[off:], marker)
		if i < 0 {
			return audio, video
		}
		start := off + i + len(marker) + 8
		if start+4 > len(head) {
			return audio, video
		}
		switch string(head[start : start+4]) {
		case "soun":
			audio = true
		case "vide":
			video = true
		}
		off += i + len(marker)
	}
}
