package examfile

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"path"
	"strings"
	"unicode/utf8"
)

var (
	errUnreadableZip = &Error{Msg: "zip file could not be read"}
	errUnsafeZipPath = &Error{Msg: "zip contains an unsafe file path"}
)

// ReadUpload validates an uploaded file and returns its exam text and,
// for a zip, the other files it contains. Errors are *Error.
func ReadUpload(data []byte) (Upload, error) {
	if len(data) == 0 {
		return Upload{}, &Error{Msg: "upload is empty"}
	}
	if !bytes.HasPrefix(data, []byte("PK\x03\x04")) && !bytes.HasPrefix(data, []byte("PK\x05\x06")) {
		text, err := checkText(data)
		if err != nil {
			return Upload{}, err
		}
		return Upload{Text: text, Dir: ".", Files: map[string][]byte{}}, nil
	}
	return readZip(data)
}

func readZip(data []byte) (Upload, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if errors.Is(err, zip.ErrInsecurePath) {
		return Upload{}, errUnsafeZipPath
	}
	if err != nil {
		return Upload{}, errUnreadableZip
	}
	for _, f := range zr.File {
		if unsafeZipName(f.Name) {
			return Upload{}, errUnsafeZipPath
		}
	}

	var entries []*zip.File
	for _, f := range zr.File {
		if skipZipEntry(f) {
			continue
		}
		entries = append(entries, f)
	}
	if len(entries) > MaxZipFiles {
		return Upload{}, errorf("zip has more than %d files", MaxZipFiles)
	}

	files := make(map[string][]byte, len(entries))
	var textKeys []string
	total := 0
	for _, f := range entries {
		key := path.Clean(f.Name)
		content, err := readZipEntry(f)
		if err != nil {
			return Upload{}, err
		}
		if len(content) > MaxZipEntryBytes {
			return Upload{}, errorf("zip file \"%s\" is larger than 5 MB", key)
		}
		total += len(content)
		if total > MaxZipTotalBytes {
			return Upload{}, &Error{Msg: "zip contents are larger than 20 MB in total"}
		}
		if ext := strings.ToLower(path.Ext(key)); ext == ".md" || ext == ".txt" {
			textKeys = append(textKeys, key)
		}
		files[key] = content
	}
	if len(textKeys) != 1 {
		return Upload{}, errorf("zip must contain exactly one .md or .txt exam file, found %d", len(textKeys))
	}

	textKey := textKeys[0]
	text, err := checkText(files[textKey])
	if err != nil {
		return Upload{}, err
	}
	delete(files, textKey)
	return Upload{Text: text, Dir: path.Dir(textKey), Files: files}, nil
}

// unsafeZipName reports names that could escape the archive root. The check
// is done here because zip.ErrInsecurePath is not enabled by default.
func unsafeZipName(name string) bool {
	if strings.Contains(name, `\`) || strings.HasPrefix(name, "/") {
		return true
	}
	clean := path.Clean(name)
	return clean == ".." || strings.HasPrefix(clean, "../")
}

// skipZipEntry drops directories, symlinks and other non-regular entries,
// macOS resource forks and hidden files.
func skipZipEntry(f *zip.File) bool {
	if !f.Mode().IsRegular() {
		return true
	}
	clean := path.Clean(f.Name)
	if strings.HasPrefix(path.Base(clean), ".") {
		return true
	}
	for seg := range strings.SplitSeq(clean, "/") {
		if seg == "__MACOSX" {
			return true
		}
	}
	return false
}

// readZipEntry reads at most MaxZipEntryBytes+1 bytes, never trusting the
// sizes recorded in the zip headers.
func readZipEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, errUnreadableZip
	}
	defer rc.Close()
	content, err := io.ReadAll(io.LimitReader(rc, MaxZipEntryBytes+1))
	if err != nil {
		return nil, errUnreadableZip
	}
	return content, nil
}

func checkText(text []byte) ([]byte, error) {
	text = bytes.TrimPrefix(text, []byte("\uFEFF"))
	if len(text) > MaxTextBytes {
		return nil, &Error{Msg: "exam text is larger than 1 MB"}
	}
	if !utf8.Valid(text) {
		return nil, &Error{Msg: "exam text is not valid UTF-8"}
	}
	return text, nil
}
