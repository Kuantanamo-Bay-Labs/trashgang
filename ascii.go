// ASCCII FROM IMAGEEEEEEEEEEEEEEEEEEEE
package main

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	xdraw "golang.org/x/image/draw"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	// need the webp and bmp formats too~ :3
	_ "golang.org/x/image/bmp"
	"golang.org/x/image/webp"
)

// describes render info returned to main.go
type AsciiMeta struct {
	Source       string
	OrigW, OrigH int
	OutW, OutH   int
}

const (
	maxImageBytes    = 15 << 20 // 15 MiB safety cap
	httpTimeout      = 12 * time.Second
	asciiCellAspectY = 0.50 // ASCII characters are ~2x taller than wide; tweak if needed
	defaultUserAgent = "TrashGang-ASCII/1.0 (+https://charm.sh/wish)"
)

// WIP!!! Loads an image from local path or URL and converts to ASCII~
func asciiFromSource(src string, outWidth int, colorize, invert bool, charset string) (string, AsciiMeta, error) {
	if outWidth < 8 {
		outWidth = 8
	}
	if outWidth > 400 {
		outWidth = 400
	}

	data, sourceLabel, err := loadBytes(src)
	if err != nil {
		return "", AsciiMeta{}, err
	}

	img, format, err := decodeImage(data)
	if err != nil {
		return "", AsciiMeta{}, fmt.Errorf("decode error (%s): %w", format, err)
	}

	b := img.Bounds()
	origW, origH := b.Dx(), b.Dy()
	if origW == 0 || origH == 0 {
		return "", AsciiMeta{}, errors.New("empty image")
	}

	outHeight := int(float64(outWidth) * float64(origH) / float64(origW) * asciiCellAspectY)
	if outHeight < 1 {
		outHeight = 1
	}

	// Scale to target size~~~
	dst := image.NewRGBA(image.Rect(0, 0, outWidth, outHeight))
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, img.Bounds(), xdraw.Over, nil)

	// Build ASCII art ^w^
	block := rasterToASCII(dst, colorize, invert, charset)

	meta := AsciiMeta{
		Source: sourceLabel,
		OrigW:  origW, OrigH: origH,
		OutW: outWidth, OutH: outHeight,
	}
	return block, meta, nil
}

// reads from file path or HTTP(S) URL.
func loadBytes(src string) ([]byte, string, error) {
	if isHTTP(src) {
		bs, err := fetchURL(src)
		return bs, src, err
	}
	// Local path (expand "~" crude support)
	if strings.HasPrefix(src, "~"+string(os.PathSeparator)) {
		if home, _ := os.UserHomeDir(); home != "" {
			src = filepath.Join(home, strings.TrimPrefix(src, "~"+string(os.PathSeparator)))
		}
	}
	f, err := os.Open(src)
	if err != nil {
		return nil, src, err
	}
	defer f.Close()

	lr := &io.LimitedReader{R: f, N: maxImageBytes + 1}
	bs, err := io.ReadAll(lr)
	if err != nil {
		return nil, src, err
	}
	if int64(len(bs)) > maxImageBytes {
		return nil, src, fmt.Errorf("file too large (> %d bytes)", maxImageBytes)
	}
	return bs, src, nil
}

func isHTTP(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func fetchURL(u string) ([]byte, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", defaultUserAgent)
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*;q=0.8,*/*;q=0.5")

	client := &http.Client{
		Timeout: httpTimeout,
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("http status %d %s", resp.StatusCode, resp.Status)
	}

	var r io.Reader = resp.Body
	if resp.ContentLength < 0 || resp.ContentLength > maxImageBytes {
		r = &io.LimitedReader{R: resp.Body, N: maxImageBytes + 1}
	}
	bs, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if int64(len(bs)) > maxImageBytes {
		return nil, fmt.Errorf("remote image too large (> %d bytes)", maxImageBytes)
	}
	return bs, nil
}

func decodeImage(bs []byte) (image.Image, string, error) {
	br := bytes.NewReader(bs)
	img, format, err := image.Decode(br)
	if err == nil {
		return img, format, nil
	}
	if img2, err2 := webp.Decode(bytes.NewReader(bs)); err2 == nil {
		return img2, "webp", nil
	}
	return nil, format, err
}

// converts a small RGBA image to ASCII string. ^w^
func rasterToASCII(img *image.RGBA, colorize, invert bool, charset string) string {
	var sb strings.Builder
	w, h := img.Rect.Dx(), img.Rect.Dy()
	if w == 0 || h == 0 {
		return ""
	}
	if charset == "" {
		charset = "@%#*+=-:. "
	}
	runes := []rune(charset)
	rampLen := len(runes)
	if rampLen < 2 {
		runes = []rune{'#', ' '}
		rampLen = 2
	}

	for y := 0; y < h; y++ {
		off := img.PixOffset(0, y)
		row := img.Pix[off : off+w*4]
		for x := 0; x < w; x++ {
			i := x * 4
			r, g, b, a := row[i+0], row[i+1], row[i+2], row[i+3]

			rr := uint32(r) * uint32(a) / 255
			gg := uint32(g) * uint32(a) / 255
			bb := uint32(b) * uint32(a) / 255

			lum := 0.2126*float64(rr) + 0.7152*float64(gg) + 0.0722*float64(bb)
			if invert {
				lum = 255.0 - lum
			}
			idx := int(lum * float64(rampLen-1) / 255.0)
			if idx < 0 {
				idx = 0
			} else if idx >= rampLen {
				idx = rampLen - 1
			}
			ch := runes[idx]

			if colorize {
				sb.WriteString(sgrColor(uint8(rr), uint8(gg), uint8(bb)))
				sb.WriteRune(ch)
			} else {
				sb.WriteRune(ch)
			}
		}
		if colorize {
			sb.WriteString("\x1b[0m")
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

func sgrColor(r, g, b uint8) string {
	// 24-bit truecolor foreground~
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
}

// Optional utility: average color of an image (unused for now!!)
func averageColor(img image.Image) color.RGBA {
	b := img.Bounds()
	var r, g, bsum, n uint64
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			r += uint64(cr)
			g += uint64(cg)
			bsum += uint64(cb)
			n++
		}
	}
	if n == 0 {
		return color.RGBA{0, 0, 0, 255}
	}
	return color.RGBA{
		uint8((r / n) >> 8),
		uint8((g / n) >> 8),
		uint8((bsum / n) >> 8),
		255,
	}
}
