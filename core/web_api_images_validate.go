//go:build webpanel && !android

package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/big"
)

const WebAPIImageMaxBytes = 32 << 20
const WebAPIImageJSONMaxBytes = 48 << 20
const webImageMaxPixels = 16 << 20
const webImageMaxFramePixels = 64 << 20
const webImageMaxFrames = 256

func webImageDimensions(width, height int) bool {
	return width > 0 && height > 0 && width <= 16384 && height <= 16384 && int64(width)*int64(height) <= webImageMaxPixels
}

func webImageInvalid() error {
	return webAPIError(422, "invalid_icon_image", "source is not a supported, safe and bounded image")
}

func webImageSourceID(raw string) string {
	hash := sha256.Sum256([]byte(raw))
	return new(big.Int).SetBytes(hash[:]).String()
}

func webImageSHA(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func webImageValidate(data []byte) (string, error) {
	if len(data) == 0 || len(data) > WebAPIImageMaxBytes {
		return "", webAPIError(413, "image_too_large", "icon image exceeds the resource limit")
	}
	reader := bytes.NewReader(data)
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		config, err := png.DecodeConfig(reader)
		if err != nil || !webImageDimensions(config.Width, config.Height) || !webImagePNGFrames(data) {
			return "", webImageInvalid()
		}
		reader.Reset(data)
		if _, err = png.Decode(reader); err != nil || reader.Len() != 0 {
			return "", webImageInvalid()
		}
		return "image/png", nil
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		config, err := jpeg.DecodeConfig(reader)
		if err != nil || !webImageDimensions(config.Width, config.Height) || !webImageJPEGBoundary(data, webImageMaxFramePixels/(config.Width*config.Height)) {
			return "", webImageInvalid()
		}
		reader.Reset(data)
		if _, err = jpeg.Decode(reader); err != nil || reader.Len() != 0 {
			return "", webImageInvalid()
		}
		return "image/jpeg", nil
	case bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a")):
		if !webImageGIFBudget(data) {
			return "", webImageInvalid()
		}
		if _, err := gif.DecodeAll(reader); err != nil || reader.Len() != 0 {
			return "", webImageInvalid()
		}
		return "image/gif", nil
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return webImageValidateWebP(data)
	default:
		if err := webImageValidateSVG(data); err != nil {
			return "", err
		}
		return "image/svg+xml", nil
	}
}

func webImageGIFBudget(data []byte) bool {
	if len(data) < 13 {
		return false
	}
	width, height := int(binary.LittleEndian.Uint16(data[6:])), int(binary.LittleEndian.Uint16(data[8:]))
	if !webImageDimensions(width, height) {
		return false
	}
	offset := 13
	if data[10]&0x80 != 0 {
		offset += 3 << ((data[10] & 7) + 1)
	}
	frames, pixels := 0, int64(0)
	blocks := func() bool {
		for offset < len(data) {
			size := int(data[offset])
			offset++
			if size == 0 {
				return true
			}
			offset += size
			if offset > len(data) {
				return false
			}
		}
		return false
	}
	for offset < len(data) {
		kind := data[offset]
		offset++
		switch kind {
		case 0x3b:
			return offset == len(data) && frames > 0
		case 0x21:
			if offset >= len(data) {
				return false
			}
			offset++
			if !blocks() {
				return false
			}
		case 0x2c:
			if offset+9 > len(data) {
				return false
			}
			w := int(binary.LittleEndian.Uint16(data[offset+4:]))
			h := int(binary.LittleEndian.Uint16(data[offset+6:]))
			if !webImageDimensions(w, h) {
				return false
			}
			frames++
			pixels += int64(w) * int64(h)
			if frames > webImageMaxFrames || pixels > webImageMaxFramePixels {
				return false
			}
			packed := data[offset+8]
			offset += 9
			if packed&0x80 != 0 {
				offset += 3 << ((packed & 7) + 1)
			}
			if offset >= len(data) {
				return false
			}
			offset++
			if !blocks() {
				return false
			}
		default:
			return false
		}
	}
	return false
}
