package media

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func webm(codecs ...string) []byte {
	b := []byte("\x1a\x45\xdf\xa3\x9f\x42\x86\x81\x01\x42\x82\x84webm")
	b = append(b, "\x18\x53\x80\x67\x01\xff\xff\xff\xff\xff\xff\xff\x16\x54\xae\x6b"...)
	for _, c := range codecs {
		b = append(b, 0xae, 0x80|byte(len(c)+2), 0x86, 0x80|byte(len(c)))
		b = append(b, c...)
	}
	return append(b, make([]byte, 64)...)
}

func box(kind string, payload []byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(8+len(payload)))
	return append(append(b, kind...), payload...)
}

func mp4(brands string, handlers ...string) []byte {
	ftyp := box("ftyp", []byte(brands[:4]+"\x00\x00\x02\x00"+brands))
	var traks []byte
	for _, h := range handlers {
		hdlr := box("hdlr", append([]byte("\x00\x00\x00\x00\x00\x00\x00\x00"+h), make([]byte, 13)...))
		traks = append(traks, box("trak", box("mdia", hdlr))...)
	}
	return append(ftyp, box("moov", traks)...)
}

func ogg(firstPacket string) []byte {
	b := []byte("OggS\x00\x02\x00\x00\x00\x00\x00\x00\x00\x00\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x01\x13")
	return append(b, firstPacket...)
}

func TestSniffRefinesRecorderContainers(t *testing.T) {
	cases := []struct {
		name, mime, kind string
		data             []byte
	}{
		{"chrome webm voice", "audio/webm", "audio", webm("A_OPUS")},
		{"webm video with audio", "video/webm", "video", webm("V_VP8", "A_OPUS")},
		{"webm without visible tracks", "video/webm", "video", webm()},
		{"safari mp4 voice", "audio/mp4", "audio", mp4("isommp41", "soun")},
		{"mp4 video", "video/mp4", "video", mp4("isommp41", "vide", "soun")},
		{"mp4 with late moov", "video/mp4", "video", mp4("isommp41")},
		{"m4a brand only", "audio/mp4", "audio", mp4("M4A isom")},
		{"m4a brand with mp4 compat", "audio/mp4", "audio", mp4("M4A mp42")},
		{"iphone mov with late moov", "video/quicktime", "video", mp4("qt  qt  ")},
		{"iphone mov", "video/quicktime", "video", mp4("qt  qt  ", "vide", "soun")},
		{"audio-only mov", "audio/mp4", "audio", mp4("qt  qt  ", "soun")},
		{"unknown ftyp brand", "application/octet-stream", "file", mp4("heicmif1")},
		{"firefox ogg opus", "audio/ogg", "audio", ogg("OpusHead\x01\x01")},
		{"ogg vorbis", "audio/ogg", "audio", ogg("\x01vorbis")},
		{"ogg theora", "video/ogg", "video", ogg("\x80theora")},
		{"unknown ogg", "application/ogg", "file", ogg("mystery")},
		{"png", "image/png", "image", []byte("\x89PNG\x0d\x0a\x1a\x0a" + string(make([]byte, 32)))},
		{"svg is never inline", "text/xml; charset=utf-8", "file", []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`)},
		{"binary", "application/octet-stream", "file", []byte{0, 1, 2, 3, 0x86, 0x86, 'A', '_', 'O', 'P', 'U', 'S'}},
	}
	for _, c := range cases {
		mime, kind := Sniff(c.data)
		if mime != c.mime || kind != c.kind {
			t.Errorf("%s: Sniff = %q/%q, want %q/%q", c.name, mime, kind, c.mime, c.kind)
		}
	}
}

func TestSniffInspectsBeyond512Bytes(t *testing.T) {
	data := mp4("isommp41")
	data = append(data, box("free", make([]byte, 4096))...)
	data = append(data, mp4("isommp41", "soun")[len(box("ftyp", make([]byte, 16))):]...)
	if mime, kind := Sniff(data); mime != "audio/mp4" || kind != "audio" {
		t.Fatalf("Sniff = %q/%q, want audio/mp4", mime, kind)
	}
	if mime, _ := Sniff(append(data[:0:0], data[:512]...)); mime != "video/mp4" {
		t.Fatalf("truncated head = %q, want video/mp4", mime)
	}
}

func TestSniffIgnoresBytesPastLimit(t *testing.T) {
	data := append(webm(), bytes.Repeat([]byte{0}, SniffLen)...)
	data = append(data, 0x86, 0x86, 'A', '_', 'O', 'P', 'U', 'S')
	if mime, _ := Sniff(data); mime != "video/webm" {
		t.Fatalf("Sniff = %q, want video/webm", mime)
	}
}
