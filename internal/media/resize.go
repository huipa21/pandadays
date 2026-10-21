package media

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"

	xdraw "golang.org/x/image/draw"
)

const (
	maxImageDimension = 2048
	maxImagePixels    = int64(40_000_000)
	jpegQuality       = 82
)

func (s *Store) resizeImage(st *staged) error {
	if st.mime != "image/jpeg" && st.mime != "image/png" {
		return nil
	}
	src, err := os.Open(st.temp)
	if err != nil {
		return fmt.Errorf("open staged image: %w", err)
	}
	config, _, err := image.DecodeConfig(src)
	if closeErr := src.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("%w: decode image dimensions: %w", ErrUploadStream, err)
	}
	if config.Width <= 0 || config.Height <= 0 ||
		int64(config.Width) > maxImagePixels/int64(config.Height) {
		return ErrImageTooLarge
	}

	src, err = os.Open(st.temp)
	if err != nil {
		return fmt.Errorf("open staged image: %w", err)
	}
	decoded, _, decodeErr := image.Decode(src)
	closeErr := src.Close()
	if decodeErr != nil {
		return fmt.Errorf("%w: decode image: %w", ErrUploadStream, decodeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close staged image: %w", closeErr)
	}

	if st.mime == "image/jpeg" {
		orientation, err := jpegOrientation(st.temp)
		if err != nil {
			return fmt.Errorf("read JPEG orientation: %w", err)
		}
		decoded = orientImage(decoded, orientation)
	}

	bounds := decoded.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width > maxImageDimension || height > maxImageDimension {
		if width >= height {
			height = max(1, (height*maxImageDimension+width/2)/width)
			width = maxImageDimension
		} else {
			width = max(1, (width*maxImageDimension+height/2)/height)
			height = maxImageDimension
		}
	}
	resized := image.NewRGBA(image.Rect(0, 0, width, height))
	xdraw.CatmullRom.Scale(resized, resized.Bounds(), decoded, bounds, xdraw.Src, nil)

	temp, err := s.CreateTemp(".resize-*")
	if err != nil {
		return fmt.Errorf("create resized image: %w", err)
	}
	encodeErr := error(nil)
	if st.mime == "image/jpeg" {
		encodeErr = jpeg.Encode(temp, resized, &jpeg.Options{Quality: jpegQuality})
	} else {
		encodeErr = png.Encode(temp, resized)
	}
	if encodeErr != nil {
		closeErr := temp.Close()
		removeErr := os.Remove(temp.Name())
		return errors.Join(fmt.Errorf("encode resized image: %w", encodeErr), closeErr, removeErr)
	}
	if err := temp.Close(); err != nil {
		return errors.Join(fmt.Errorf("close resized image: %w", err), os.Remove(temp.Name()))
	}

	sum, size, err := hashFile(temp.Name())
	if err != nil {
		return errors.Join(err, os.Remove(temp.Name()))
	}
	if err := os.Remove(st.temp); err != nil {
		return errors.Join(fmt.Errorf("replace staged image: %w", err), os.Remove(temp.Name()))
	}
	st.temp = temp.Name()
	st.sum = sum
	st.size = size
	return nil
}

func hashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("open processed image: %w", err)
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return "", 0, errors.Join(wrapError("hash processed image", err), wrapError("close processed image", closeErr))
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func wrapError(message string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", message, err)
}

func jpegOrientation(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 1, err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	var start [2]byte
	if _, err := io.ReadFull(reader, start[:]); err != nil {
		return 1, err
	}
	if start != [2]byte{0xff, 0xd8} {
		return 1, nil
	}
	for {
		markerStart, err := reader.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 1, nil
			}
			return 1, err
		}
		if markerStart != 0xff {
			continue
		}
		marker, err := reader.ReadByte()
		for err == nil && marker == 0xff {
			marker, err = reader.ReadByte()
		}
		if err != nil {
			return 1, nil
		}
		if marker == 0xda || marker == 0xd9 {
			return 1, nil
		}
		if marker == 0x01 || marker >= 0xd0 && marker <= 0xd7 {
			continue
		}
		var lengthBytes [2]byte
		if _, err := io.ReadFull(reader, lengthBytes[:]); err != nil {
			return 1, nil
		}
		length := int(binary.BigEndian.Uint16(lengthBytes[:]))
		if length < 2 {
			return 1, nil
		}
		segment := make([]byte, length-2)
		if _, err := io.ReadFull(reader, segment); err != nil {
			return 1, nil
		}
		if marker == 0xe1 {
			if orientation := parseEXIFOrientation(segment); orientation != 0 {
				return orientation, nil
			}
		}
	}
}

func parseEXIFOrientation(segment []byte) int {
	if len(segment) < 14 || string(segment[:6]) != "Exif\x00\x00" {
		return 0
	}
	tiff := segment[6:]
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 0
	}
	if order.Uint16(tiff[2:4]) != 42 {
		return 0
	}
	ifdOffset := uint64(order.Uint32(tiff[4:8]))
	if ifdOffset+2 > uint64(len(tiff)) {
		return 0
	}
	offset := int(ifdOffset)
	count := int(order.Uint16(tiff[offset : offset+2]))
	for i := 0; i < count; i++ {
		entry := offset + 2 + i*12
		if entry+12 > len(tiff) {
			return 0
		}
		if order.Uint16(tiff[entry:entry+2]) != 0x0112 ||
			order.Uint16(tiff[entry+2:entry+4]) != 3 ||
			order.Uint32(tiff[entry+4:entry+8]) < 1 {
			continue
		}
		value := int(order.Uint16(tiff[entry+8 : entry+10]))
		if value >= 1 && value <= 8 {
			return value
		}
	}
	return 0
}

func orientImage(src image.Image, orientation int) image.Image {
	if orientation < 2 || orientation > 8 {
		return src
	}
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	outWidth, outHeight := width, height
	if orientation >= 5 {
		outWidth, outHeight = height, width
	}
	dst := image.NewRGBA(image.Rect(0, 0, outWidth, outHeight))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			dx, dy := x, y
			switch orientation {
			case 2:
				dx = width - 1 - x
			case 3:
				dx, dy = width-1-x, height-1-y
			case 4:
				dy = height - 1 - y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = height-1-y, x
			case 7:
				dx, dy = height-1-y, width-1-x
			case 8:
				dx, dy = y, width-1-x
			}
			dst.Set(dx, dy, src.At(bounds.Min.X+x, bounds.Min.Y+y))
		}
	}
	return dst
}
