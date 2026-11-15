//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strconv"
	"strings"
	"testing"
)

func webImageTestPNG(t *testing.T) []byte {
	t.Helper()
	var body bytes.Buffer
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.NRGBA{R: 255, A: 255})
	if err := png.Encode(&body, img); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func webImageTestLargePNG(t *testing.T, size int) []byte {
	t.Helper()
	base := webImageTestPNG(t)
	payload := make([]byte, size-len(base)-12)
	copy(payload, "padding\x00")
	chunk := make([]byte, len(payload)+12)
	binary.BigEndian.PutUint32(chunk, uint32(len(payload)))
	copy(chunk[4:], "tEXt")
	copy(chunk[8:], payload)
	binary.BigEndian.PutUint32(chunk[len(chunk)-4:], crc32.ChecksumIEEE(chunk[4:len(chunk)-4]))
	result := append([]byte{}, base[:len(base)-12]...)
	result = append(result, chunk...)
	return append(result, base[len(base)-12:]...)
}

func webImageTestAPNG(t *testing.T, frames int) []byte {
	t.Helper()
	base := webImageTestPNG(t)
	header := base[16:29]
	var idat []byte
	for offset := 8; offset+12 <= len(base); {
		size := int(binary.BigEndian.Uint32(base[offset:]))
		kind := string(base[offset+4 : offset+8])
		if kind == "IDAT" {
			idat = append(idat, base[offset+8:offset+8+size]...)
		}
		offset += size + 12
	}
	result := append([]byte{}, base[:8]...)
	result = append(result, webImagePNGChunk("IHDR", header)...)
	actl := make([]byte, 8)
	binary.BigEndian.PutUint32(actl, uint32(frames))
	result = append(result, webImagePNGChunk("acTL", actl)...)
	sequence := uint32(0)
	for frame := 0; frame < frames; frame++ {
		control := make([]byte, 26)
		binary.BigEndian.PutUint32(control, sequence)
		sequence++
		binary.BigEndian.PutUint32(control[4:], 2)
		binary.BigEndian.PutUint32(control[8:], 2)
		control[20] = 1
		control[22] = 10
		result = append(result, webImagePNGChunk("fcTL", control)...)
		if frame == 0 {
			result = append(result, webImagePNGChunk("IDAT", idat)...)
		} else {
			payload := make([]byte, 4)
			binary.BigEndian.PutUint32(payload, sequence)
			sequence++
			payload = append(payload, idat...)
			result = append(result, webImagePNGChunk("fdAT", payload)...)
		}
	}
	return append(result, webImagePNGChunk("IEND", nil)...)
}

func TestWebImageValidateRealRasterAndBoundedSVG(t *testing.T) {
	pngBytes := webImageTestPNG(t)
	var jpegBytes, gifBytes bytes.Buffer
	if err := jpeg.Encode(&jpegBytes, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatal(err)
	}
	if err := gif.Encode(&gifBytes, image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White}), nil); err != nil {
		t.Fatal(err)
	}
	webpBytes, err := base64.StdEncoding.DecodeString("UklGRhwAAABXRUJQVlA4TA8AAAAvAAAAAAcQ/Y/+ByKi/wEA")
	if err != nil {
		t.Fatal(err)
	}
	animatedWebP, err := base64.StdEncoding.DecodeString("UklGRoQAAABXRUJQVlA4WAoAAAASAAAAAQAAAQAAQU5JTQYAAAAAAAAAAABBTk1GKAAAAAAAAAAAAAEAAAEAAGQAAAJWUDhMDwAAAC8BQAAQBxD9j+IFIqL/AQBBTk1GKAAAAAAAAAAAAAEAAAEAAGQAAABWUDhMDwAAAC8BQAAABxDR//4HIqL/AQA=")
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		media string
		body  []byte
	}{
		{"image/png", pngBytes}, {"image/png", webImageTestAPNG(t, 2)}, {"image/jpeg", jpegBytes.Bytes()}, {"image/gif", gifBytes.Bytes()}, {"image/webp", webpBytes}, {"image/webp", animatedWebP},
		{"image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><defs><linearGradient id="a"><stop offset="0" stop-color="#fff"/></linearGradient></defs><path fill="url(#a)" d="M0 0h24v24z"/></svg>`)},
		{"image/svg+xml", []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="24" height="24"><defs><path id="a" d="M0 0h1"/></defs><use xlink:href="#a"/></svg>`)},
	} {
		t.Run(fixture.media, func(t *testing.T) {
			got, err := webImageValidate(fixture.body)
			if err != nil || got != fixture.media {
				t.Fatalf("media %s, err %v", got, err)
			}
		})
	}
	jpegPolyglot := append(append([]byte{}, jpegBytes.Bytes()...), []byte(`{"secret":"metadata"}`)...)
	jpegPolyglot = append(jpegPolyglot, 0xff, 0xd9)
	if _, err := webImageValidate(jpegPolyglot); err == nil {
		t.Fatal("accepted JPEG metadata trailer with a forged end marker")
	}
	for _, raster := range [][]byte{pngBytes, jpegBytes.Bytes(), gifBytes.Bytes(), webpBytes} {
		if _, err := webImageValidate(append(append([]byte{}, raster...), []byte(`{"secret":"metadata"}`)...)); err == nil {
			t.Fatal("accepted trailing non-image body")
		}
	}
}

func TestWebImageValidateRejectsActiveSVGAndMetadataExfiltration(t *testing.T) {
	for _, body := range []string{
		`{"secret":"body"}`, `not an image`, `<html><img src="/api/v1/meta"/></html>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><body/></foreignObject></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><image href="http://169.254.169.254/latest/meta-data"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><use href="https://evil.test/icon.svg#a"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><style>@import "https://evil.test/style";</style></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><path style="fill:url(//evil.test/x)"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><style>path{fill:u&#114;l(//evil.test/x)}</style></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><style>path{fill:u<!-- split -->rl(//evil.test/x)}</style></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><style>path{fill:image-set('/metadata')}</style></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><path style="fill:u\72l(/metadata)"/></svg>`,
		`<!DOCTYPE svg [<!ENTITY secret SYSTEM "file:///data/private">]><svg xmlns="http://www.w3.org/2000/svg"><text>&secret;</text></svg>`,
		`<?xml-stylesheet href="https://evil.test/x"?><svg xmlns="http://www.w3.org/2000/svg"/>`,
		`<svg xmlns="http://www.w3.org/2000/svg" xml:base="https://evil.test/"><use href="#x"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><g id="loop"><use href="#loop"/></g></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg" width="999999" height="999999"/>`,
		`<svg xmlns="http://www.w3.org/2000/svg"/><svg xmlns="http://www.w3.org/2000/svg"/>`,
	} {
		if _, err := webImageValidate([]byte(body)); err == nil {
			t.Fatalf("accepted active/non-image body %s", body)
		}
	}
}

func TestWebImageValidateBudgetsAndFullSHAIdentity(t *testing.T) {
	large := webImageTestLargePNG(t, WebAPIImageMaxBytes)
	if got, err := webImageValidate(large); err != nil || got != "image/png" {
		t.Fatalf("32MiB real image refused: %s %v", got, err)
	}
	if _, err := webImageValidate(append(large, 0)); err == nil {
		t.Fatal("unbounded image accepted")
	}
	if _, err := webImageValidate(webImageTestAPNG(t, webImageMaxFrames+1)); err == nil {
		t.Fatal("APNG frame budget not enforced")
	}
	var frames bytes.Buffer
	frame := image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.Black, color.White})
	animation := gif.GIF{}
	for i := 0; i <= webImageMaxFrames; i++ {
		animation.Image = append(animation.Image, frame)
		animation.Delay = append(animation.Delay, 1)
	}
	if err := gif.EncodeAll(&frames, &animation); err != nil {
		t.Fatal(err)
	}
	if _, err := webImageValidate(frames.Bytes()); err == nil {
		t.Fatal("too many frames accepted")
	}
	var svg strings.Builder
	svg.WriteString(`<svg xmlns="http://www.w3.org/2000/svg"><defs><path id="n0" d="M0 0h1"/>`)
	for i := 1; i <= 24; i++ {
		svg.WriteString(`<g id="n` + strconv.Itoa(i) + `"><use href="#n` + strconv.Itoa(i-1) + `"/><use href="#n` + strconv.Itoa(i-1) + `"/></g>`)
	}
	svg.WriteString(`</defs></svg>`)
	if _, err := webImageValidate([]byte(svg.String())); err == nil {
		t.Fatal("recursive expansion budget not enforced")
	}
	id := webImageSourceID("https://images.test/icon.svg")
	if len(id) < 70 || strings.ContainsAny(id, "/\\.abcdefghijklmnopqrstuvwxyz") || id != WebAPIUtilityIconSourceID("https://images.test/icon.svg") {
		t.Fatalf("lossy or path-based source identity: %s", id)
	}
}
