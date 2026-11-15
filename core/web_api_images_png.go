//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image/png"
)

func webImagePNGChunk(kind string, data []byte) []byte {
	result := make([]byte, len(data)+12)
	binary.BigEndian.PutUint32(result, uint32(len(data)))
	copy(result[4:], kind)
	copy(result[8:], data)
	binary.BigEndian.PutUint32(result[len(result)-4:], crc32.ChecksumIEEE(result[4:len(result)-4]))
	return result
}

func webImagePNGFrames(data []byte) bool {
	var header, palette, transparency []byte
	var frameData [][]byte
	frameW, frameH, canvasW, canvasH, controls, decoded := 0, 0, 0, 0, 0, 0
	declared := uint32(0)
	sequence := uint32(0)
	var pixels int64
	animated, haveIDAT, fdAT, idatFrame, ended := false, false, false, false, false
	decode := func() bool {
		if frameW == 0 || len(frameData) == 0 || len(header) != 13 {
			return false
		}
		h := append([]byte{}, header...)
		binary.BigEndian.PutUint32(h, uint32(frameW))
		binary.BigEndian.PutUint32(h[4:], uint32(frameH))
		frame := append([]byte{}, []byte("\x89PNG\r\n\x1a\n")...)
		frame = append(frame, webImagePNGChunk("IHDR", h)...)
		if palette != nil {
			frame = append(frame, webImagePNGChunk("PLTE", palette)...)
		}
		if transparency != nil {
			frame = append(frame, webImagePNGChunk("tRNS", transparency)...)
		}
		for _, chunk := range frameData {
			frame = append(frame, webImagePNGChunk("IDAT", chunk)...)
		}
		frame = append(frame, webImagePNGChunk("IEND", nil)...)
		if _, err := png.Decode(bytes.NewReader(frame)); err != nil {
			return false
		}
		decoded++
		frameData = nil
		return true
	}
	for offset := 8; offset+12 <= len(data); {
		size := uint64(binary.BigEndian.Uint32(data[offset:]))
		end := uint64(offset) + size + 12
		if ended || end > uint64(len(data)) {
			return false
		}
		kind := string(data[offset+4 : offset+8])
		payload := data[offset+8 : end-4]
		if crc32.ChecksumIEEE(data[offset+4:end-4]) != binary.BigEndian.Uint32(data[end-4:end]) {
			return false
		}
		switch kind {
		case "IHDR":
			if offset != 8 || len(payload) != 13 {
				return false
			}
			header = payload
			canvasW, canvasH = int(binary.BigEndian.Uint32(payload)), int(binary.BigEndian.Uint32(payload[4:]))
		case "PLTE":
			if haveIDAT {
				return false
			}
			palette = payload
		case "tRNS":
			if haveIDAT {
				return false
			}
			transparency = payload
		case "acTL":
			if animated || haveIDAT || len(payload) != 8 {
				return false
			}
			animated = true
			declared = binary.BigEndian.Uint32(payload)
			if declared == 0 || declared > webImageMaxFrames {
				return false
			}
		case "fcTL":
			if !animated || len(payload) != 26 || binary.BigEndian.Uint32(payload) != sequence || payload[24] > 2 || payload[25] > 1 {
				return false
			}
			sequence++
			if controls > 0 && !decode() {
				return false
			}
			frameW, frameH = int(binary.BigEndian.Uint32(payload[4:])), int(binary.BigEndian.Uint32(payload[8:]))
			x, y := int(binary.BigEndian.Uint32(payload[12:])), int(binary.BigEndian.Uint32(payload[16:]))
			if !webImageDimensions(frameW, frameH) || int64(x)+int64(frameW) > int64(canvasW) || int64(y)+int64(frameH) > int64(canvasH) {
				return false
			}
			controls++
			pixels += int64(frameW) * int64(frameH)
			if controls > webImageMaxFrames || pixels > webImageMaxFramePixels {
				return false
			}
			if !haveIDAT {
				idatFrame = true
				if frameW != canvasW || frameH != canvasH || x != 0 || y != 0 {
					return false
				}
			}
		case "IDAT":
			if fdAT || len(header) != 13 {
				return false
			}
			haveIDAT = true
			if animated && idatFrame && controls == 1 {
				frameData = append(frameData, payload)
			}
		case "fdAT":
			if !animated || !haveIDAT || controls == 0 || len(payload) < 4 || binary.BigEndian.Uint32(payload) != sequence {
				return false
			}
			sequence++
			fdAT = true
			frameData = append(frameData, payload[4:])
		case "IEND":
			if len(payload) != 0 || end != uint64(len(data)) {
				return false
			}
			ended = true
			if animated && (!decode() || uint32(controls) != declared || decoded != controls) {
				return false
			}
		}
		offset = int(end)
	}
	return ended && haveIDAT
}
