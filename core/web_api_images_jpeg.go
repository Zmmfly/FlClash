//go:build webpanel && !android

package core

import "encoding/binary"

func webImageJPEGBoundary(data []byte, maxScans int) bool {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return false
	}
	offset, markers := 2, 0
	scan := false
	scans := 0
	for offset < len(data) {
		markers++
		if markers > 65536 || data[offset] != 0xff {
			return false
		}
		for offset < len(data) && data[offset] == 0xff {
			offset++
		}
		if offset >= len(data) {
			return false
		}
		kind := data[offset]
		offset++
		if kind == 0xd9 {
			return scan && offset == len(data)
		}
		if kind == 0x00 || kind == 0xd8 || kind >= 0xd0 && kind <= 0xd7 || kind == 0x01 {
			return false
		}
		if offset+2 > len(data) {
			return false
		}
		size := int(binary.BigEndian.Uint16(data[offset:]))
		if size < 2 || size > len(data)-offset {
			return false
		}
		offset += size
		if kind != 0xda {
			continue
		}
		scan = true
		scans++
		if scans > maxScans || scans > webImageMaxFrames {
			return false
		}
		for offset < len(data) {
			if data[offset] != 0xff {
				offset++
				continue
			}
			start := offset
			for offset < len(data) && data[offset] == 0xff {
				offset++
			}
			if offset >= len(data) {
				return false
			}
			kind = data[offset]
			if kind == 0x00 || kind >= 0xd0 && kind <= 0xd7 {
				offset++
				continue
			}
			offset = start
			break
		}
	}
	return false
}
