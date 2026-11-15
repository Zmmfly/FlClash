//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/binary"

	"golang.org/x/image/webp"
)

func webImageWebP24(data []byte) int {
	return int(data[0]) | int(data[1])<<8 | int(data[2])<<16
}

func webImageWebPChunks(data []byte, visit func(string, []byte) bool) bool {
	for offset := 0; offset < len(data); {
		if offset+8 > len(data) {
			return false
		}
		size := uint64(binary.LittleEndian.Uint32(data[offset+4:]))
		end := uint64(offset) + 8 + size
		padded := end + size%2
		if padded > uint64(len(data)) || !visit(string(data[offset:offset+4]), data[offset+8:end]) {
			return false
		}
		offset = int(padded)
	}
	return true
}

func webImageWebPDecode(data []byte) bool {
	config, err := webp.DecodeConfig(bytes.NewReader(data))
	if err != nil || !webImageDimensions(config.Width, config.Height) {
		return false
	}
	_, err = webp.Decode(bytes.NewReader(data))
	return err == nil
}

func webImageValidateWebP(data []byte) (string, error) {
	if len(data) < 20 || uint64(binary.LittleEndian.Uint32(data[4:]))+8 != uint64(len(data)) {
		return "", webImageInvalid()
	}
	canvasW, canvasH, frames, stills, extended := 0, 0, 0, 0, 0
	var totalPixels int64
	animation := false
	valid := webImageWebPChunks(data[12:], func(kind string, payload []byte) bool {
		switch kind {
		case "VP8X":
			extended++
			if extended != 1 || len(payload) != 10 {
				return false
			}
			animation = payload[0]&2 != 0
			canvasW, canvasH = 1+webImageWebP24(payload[4:7]), 1+webImageWebP24(payload[7:10])
			return webImageDimensions(canvasW, canvasH)
		case "ANIM":
			return animation && len(payload) == 6
		case "ANMF":
			if !animation || len(payload) < 24 {
				return false
			}
			x, y := 2*webImageWebP24(payload[:3]), 2*webImageWebP24(payload[3:6])
			width, height := 1+webImageWebP24(payload[6:9]), 1+webImageWebP24(payload[9:12])
			frames++
			totalPixels += int64(width) * int64(height)
			if frames > webImageMaxFrames || totalPixels > webImageMaxFramePixels || !webImageDimensions(width, height) || x+width > canvasW || y+height > canvasH || payload[15]&0xfc != 0 {
				return false
			}
			chunks, alpha := 0, false
			if !webImageWebPChunks(payload[16:], func(kind string, _ []byte) bool {
				switch kind {
				case "ALPH":
					if alpha {
						return false
					}
					alpha = true
				case "VP8 ", "VP8L":
					chunks++
				default:
					return false
				}
				return true
			}) || chunks != 1 {
				return false
			}
			body := append([]byte{}, payload[16:]...)
			if alpha {
				header := make([]byte, 18)
				copy(header, "VP8X")
				binary.LittleEndian.PutUint32(header[4:], 10)
				header[8] = 0x10
				for index := 0; index < 3; index++ {
					header[12+index] = byte((width - 1) >> (8 * index))
					header[15+index] = byte((height - 1) >> (8 * index))
				}
				body = append(header, body...)
			}
			frame := make([]byte, 12, len(body)+12)
			copy(frame, "RIFF")
			binary.LittleEndian.PutUint32(frame[4:], uint32(len(body)+4))
			copy(frame[8:], "WEBP")
			frame = append(frame, body...)
			config, err := webp.DecodeConfig(bytes.NewReader(frame))
			return err == nil && config.Width == width && config.Height == height && webImageWebPDecode(frame)
		case "VP8 ", "VP8L":
			stills++
			return !animation && stills == 1
		case "ALPH":
			return !animation
		case "ICCP", "EXIF", "XMP ":
			return true
		default:
			return false
		}
	})
	if !valid || animation && (frames == 0 || stills != 0) || !animation && (stills != 1 || !webImageWebPDecode(data)) {
		return "", webImageInvalid()
	}
	return "image/webp", nil
}
