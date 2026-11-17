package examfile

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"testing"
)

const examMD = "# T\n1. ![a](img/a.png)\n- a *\n- b\n"

func TestReadUpload_PlainText(t *testing.T) {
	u, err := ReadUpload([]byte("\uFEFF" + examMD))
	if err != nil {
		t.Fatal(err)
	}
	if string(u.Text) != examMD || u.Dir != "." || u.Files == nil || len(u.Files) != 0 {
		t.Errorf("Upload = %+v", u)
	}
}

func TestReadUpload_Zip(t *testing.T) {
	png := pngBytes(t, colorRGB(9, 9, 9))

	t.Run("root", func(t *testing.T) {
		u, err := ReadUpload(buildZip(t,
			zipEntry{name: "exam.md", data: []byte(examMD)},
			zipEntry{name: "img/a.png", data: png},
		))
		if err != nil {
			t.Fatal(err)
		}
		want := Upload{Text: []byte(examMD), Dir: ".", Files: map[string][]byte{"img/a.png": png}}
		if !reflect.DeepEqual(u, want) {
			t.Errorf("Upload = %+v", u)
		}
		ex := mustParse(t, u)
		if len(ex.Images) != 1 || !bytes.Equal(ex.Images[0].Data, png) {
			t.Errorf("Images = %+v", ex.Images)
		}
	})

	t.Run("top-level folder with junk", func(t *testing.T) {
		u, err := ReadUpload(buildZip(t,
			zipEntry{name: "exam/", mode: fs.ModeDir | 0o755},
			zipEntry{name: "exam/EXAM.TXT", data: []byte("\uFEFF" + examMD)},
			zipEntry{name: "exam/img/a.png", data: png},
			zipEntry{name: "__MACOSX/exam/._EXAM.TXT", data: []byte("junk")},
			zipEntry{name: "exam/__MACOSX/x.md", data: []byte("junk")},
			zipEntry{name: "exam/.DS_Store", data: []byte("junk")},
			zipEntry{name: "exam/.notes.md", data: []byte("junk")},
			zipEntry{name: "exam/link.md", data: []byte("exam/EXAM.TXT"), mode: fs.ModeSymlink | 0o777},
		))
		if err != nil {
			t.Fatal(err)
		}
		want := Upload{Text: []byte(examMD), Dir: "exam", Files: map[string][]byte{"exam/img/a.png": png}}
		if !reflect.DeepEqual(u, want) {
			t.Errorf("Upload = %+v", u)
		}
		if ex := mustParse(t, u); len(ex.Images) != 1 {
			t.Errorf("Images = %+v", ex.Images)
		}
	})
}

func TestReadUpload_Rejections(t *testing.T) {
	zeros := func(n int) []byte { return make([]byte, n) }
	md := zipEntry{name: "exam.md", data: []byte(examMD)}

	corrupt := func() []byte {
		var buf bytes.Buffer
		w := zip.NewWriter(&buf)
		f, err := w.CreateHeader(&zip.FileHeader{Name: "exam.md", Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte("MARKER " + examMD)); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		return bytes.Replace(buf.Bytes(), []byte("MARKER"), []byte("marker"), 1)
	}

	tooMany := []zipEntry{md}
	for i := range MaxZipFiles {
		tooMany = append(tooMany, zipEntry{name: fmt.Sprintf("f%d.png", i), data: []byte("x")})
	}
	var tooBig []zipEntry
	for i := range 5 {
		tooBig = append(tooBig, zipEntry{name: fmt.Sprintf("z%d.bin", i), data: zeros(MaxZipEntryBytes)})
	}

	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"empty", nil, "upload is empty"},
		{"zip garbage", []byte("PK\x03\x04 this is not a zip"), "zip file could not be read"},
		{"empty-zip garbage", []byte("PK\x05\x06"), "zip file could not be read"},
		{"zip entry checksum", corrupt(), "zip file could not be read"},
		{"parent path", buildZip(t, md, zipEntry{name: "../evil.png", data: []byte("x")}), "zip contains an unsafe file path"},
		{"absolute path", buildZip(t, md, zipEntry{name: "/abs.png", data: []byte("x")}), "zip contains an unsafe file path"},
		{"backslash path", buildZip(t, md, zipEntry{name: `a\b.png`, data: []byte("x")}), "zip contains an unsafe file path"},
		{"unsafe junk path", buildZip(t, md, zipEntry{name: "__MACOSX/../../x", data: []byte("x")}), "zip contains an unsafe file path"},
		{"too many files", buildZip(t, tooMany...), "zip has more than 200 files"},
		{"entry too large", buildZip(t, md, zipEntry{name: "img/big.bin", data: zeros(MaxZipEntryBytes + 1)}), `zip file "img/big.bin" is larger than 5 MB`},
		{"total too large", buildZip(t, append([]zipEntry{md}, tooBig...)...), "zip contents are larger than 20 MB in total"},
		{"non-UTF-8 file name", buildZip(t, md, zipEntry{name: "\xe9.png", data: []byte("x")}), "zip contains a file name that is not valid UTF-8"},
		{"non-UTF-8 exam file name", buildZip(t, zipEntry{name: "\xe9/exam.md", data: []byte(examMD)}), "zip contains a file name that is not valid UTF-8"},
		{"no exam text", buildZip(t, zipEntry{name: "a.png", data: []byte("x")}), "zip must contain exactly one .md or .txt exam file, found 0"},
		{"two exam texts", buildZip(t, md, zipEntry{name: "b/notes.txt", data: []byte("x")}), "zip must contain exactly one .md or .txt exam file, found 2"},
		{"text too large", []byte("# " + strings.Repeat("x", MaxTextBytes)), "exam text is larger than 1 MB"},
		{"zip text too large", buildZip(t, zipEntry{name: "e.md", data: zeros(MaxTextBytes + 1)}), "exam text is larger than 1 MB"},
		{"invalid UTF-8", []byte("# T\xff\n"), "exam text is not valid UTF-8"},
		{"zip invalid UTF-8", buildZip(t, zipEntry{name: "e.md", data: []byte("# \xc3")}), "exam text is not valid UTF-8"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ReadUpload(c.data)
			if got := errMsg(t, err); got != c.want {
				t.Errorf("message = %q, want %q", got, c.want)
			}
		})
	}

	t.Run("text at the limit is accepted", func(t *testing.T) {
		if _, err := ReadUpload([]byte("\uFEFF" + strings.Repeat("x", MaxTextBytes))); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}
