package examfile

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io/fs"
	"strings"
	"testing"
)

// lines joins its arguments with "\n", one argument per file line.
func lines(l ...string) string { return strings.Join(l, "\n") }

// zipEntry is one member of an in-memory test zip. A zero mode means a
// plain regular file created with zip.Writer.Create defaults.
type zipEntry struct {
	name string
	data []byte
	mode fs.FileMode
}

func buildZip(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatalf("create %q: %v", e.name, err)
		}
		if _, err := f.Write(e.data); err != nil {
			t.Fatalf("write %q: %v", e.name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func solidImage(c color.Color) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for x := range 2 {
		for y := range 2 {
			img.Set(x, y, c)
		}
	}
	return img
}

func colorRGB(r, g, b uint8) color.Color { return color.RGBA{r, g, b, 255} }

func pngBytes(t *testing.T, c color.Color) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, solidImage(c)); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func jpegBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, solidImage(color.White), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func gifBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := gif.Encode(&buf, solidImage(color.Black), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// webpBytes is a minimal RIFF/WEBP header that http.DetectContentType
// recognises; it is not a decodable image, which the parser never needs.
func webpBytes() []byte {
	return []byte("RIFF\x1a\x00\x00\x00WEBPVP8 \x0e\x00\x00\x00\x30\x01\x00\x9d\x01\x2a\x01\x00\x01\x00")
}

func imageName(data []byte, ext string) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:32] + ext
}

// errMsg asserts err is an *Error and returns its message.
func errMsg(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("error %T %q is not *Error", err, err)
	}
	if e.Error() != e.Msg {
		t.Fatalf("Error() = %q, Msg = %q", e.Error(), e.Msg)
	}
	return e.Msg
}

func textUpload(s string) Upload {
	return Upload{Text: []byte(s), Dir: ".", Files: map[string][]byte{}}
}

func mustParse(t *testing.T, u Upload) Exam {
	t.Helper()
	ex, err := Parse(u, "/media/k")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return ex
}
