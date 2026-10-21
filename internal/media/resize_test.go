package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func makeLargeJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 3000, 1500))
	for y := 0; y < 1500; y++ {
		for x := 0; x < 3000; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8((x + y) % 256),
				G: uint8((x*3 + y) % 256),
				B: uint8((x + y*5) % 256),
				A: 255,
			})
		}
	}
	var data bytes.Buffer
	if err := jpeg.Encode(&data, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func addJPEGOrientation(data []byte, orientation byte) []byte {
	tiff := []byte{
		'I', 'I', 42, 0, 8, 0, 0, 0,
		1, 0, 0x12, 0x01, 3, 0, 1, 0, 0, 0,
		orientation, 0, 0, 0, 0, 0,
	}
	payload := append([]byte("Exif\x00\x00"), tiff...)
	length := len(payload) + 2
	segment := []byte{0xff, 0xe1, byte(length >> 8), byte(length)}
	segment = append(segment, payload...)
	return append(append(append([]byte{}, data[:2]...), segment...), data[2:]...)
}

func TestUploadResizesJPEGBeforeQuotaAccounting(t *testing.T) {
	f := newBlobFixture(t)
	owner := f.user(t, "owner")
	original := addJPEGOrientation(makeLargeJPEG(t), 6)
	if _, err := f.db.Exec(`UPDATE users SET quota_bytes = ?, used_bytes = 0 WHERE id = ?`, len(original)-1, owner); err != nil {
		t.Fatal(err)
	}

	attachment, err := f.store.UploadLibrary(context.Background(), owner, "camera.jpg", bytes.NewReader(original))
	if err != nil {
		t.Fatalf("upload should fit after resizing: %v", err)
	}
	if attachment.MIME != "image/jpeg" || attachment.Kind != "image" || attachment.Size >= int64(len(original)) {
		t.Fatalf("processed metadata: %+v; original size %d", attachment, len(original))
	}
	file, err := f.store.Open(attachment)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := jpeg.Decode(file)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("decode stored JPEG: %v; close: %v", err, closeErr)
	}
	if got := decoded.Bounds().Size(); got != image.Pt(1024, 2048) {
		t.Fatalf("stored dimensions = %v, want 1024x2048 after EXIF rotation", got)
	}
	if got := f.used(t, owner); got != attachment.Size {
		t.Fatalf("used_bytes = %d, want processed size %d", got, attachment.Size)
	}
}

func TestUploadKeepsPNGTransparency(t *testing.T) {
	f := newBlobFixture(t)
	owner := f.user(t, "owner")
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	img.SetNRGBA(0, 0, color.NRGBA{R: 240, G: 20, B: 30, A: 0})
	img.SetNRGBA(1, 0, color.NRGBA{R: 10, G: 80, B: 200, A: 255})
	var original bytes.Buffer
	if err := png.Encode(&original, img); err != nil {
		t.Fatal(err)
	}
	attachment, err := f.store.UploadLibrary(context.Background(), owner, "transparent.png", bytes.NewReader(original.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	file, err := f.store.Open(attachment)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(file)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("decode stored PNG: %v; close: %v", err, closeErr)
	}
	if got := decoded.Bounds().Size(); got != image.Pt(2, 1) {
		t.Fatalf("PNG dimensions = %v, want 2x1", got)
	}
	_, _, _, alpha := decoded.At(0, 0).RGBA()
	if alpha != 0 {
		t.Fatalf("transparent pixel alpha = %d, want 0", alpha)
	}
}

func TestPrepareUploadRejectsInvalidAndOversizedImages(t *testing.T) {
	f := newBlobFixture(t)
	for name, content := range map[string][]byte{
		"invalid": {0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a},
	} {
		if _, err := f.store.PrepareUpload(bytes.NewReader(content)); err == nil {
			t.Fatalf("%s image was accepted", name)
		}
	}

	largeConfig := []byte("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], 7000)
	binary.BigEndian.PutUint32(ihdr[4:8], 6000)
	ihdr[8], ihdr[9] = 8, 6
	largeConfig = binary.BigEndian.AppendUint32(largeConfig, uint32(len(ihdr)))
	largeConfig = append(largeConfig, "IHDR"...)
	largeConfig = append(largeConfig, ihdr...)
	largeConfig = binary.BigEndian.AppendUint32(largeConfig, crc32.ChecksumIEEE(append([]byte("IHDR"), ihdr...)))
	if _, err := f.store.PrepareUpload(bytes.NewReader(largeConfig)); err != ErrImageTooLarge {
		t.Fatalf("oversized image dimensions error = %v, want ErrImageTooLarge", err)
	}
}

func TestPreparedUploadCanBeClosed(t *testing.T) {
	f := newBlobFixture(t)
	prepared, err := f.store.PrepareUpload(bytes.NewReader([]byte("plain text")))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.files(t)) != 1 {
		t.Fatal("prepared upload was not staged")
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	if len(f.files(t)) != 0 {
		t.Fatal("closing prepared upload did not remove staged file")
	}
}
