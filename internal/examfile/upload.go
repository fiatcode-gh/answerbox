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
	errUnreadableZip = NewError(CodeZipUnreadable, 0)
	errUnsafeZipPath = NewError(CodeZipUnsafePath, 0)
	errNonUTF8Name   = NewError(CodeZipNameNotUTF8, 0)
)

// ReadUpload validates an uploaded file and returns its exam text and,
// for a zip, the other files it contains. Errors are *Error.
func ReadUpload(data []byte) (Upload, error) {
	if len(data) == 0 {
		return Upload{}, NewError(CodeUploadEmpty, 0)
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
		// A name that is not UTF-8 would not survive the revision's JSON.
		if !utf8.ValidString(f.Name) {
			return Upload{}, errNonUTF8Name
		}
		entries = append(entries, f)
	}
	if len(entries) > MaxZipFiles {
		return Upload{}, NewError(CodeZipTooManyFiles, 0, MaxZipFiles)
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
			return Upload{}, NewError(CodeZipEntryTooLarge, 0, key)
		}
		total += len(content)
		if total > MaxZipTotalBytes {
			return Upload{}, NewError(CodeZipTotalTooLarge, 0)
		}
		if ext := strings.ToLower(path.Ext(key)); ext == ".md" || ext == ".txt" {
			textKeys = append(textKeys, key)
		}
		files[key] = content
	}
	if len(textKeys) != 1 {
		return Upload{}, NewError(CodeZipTextCount, 0, len(textKeys))
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
		return nil, NewError(CodeTextTooLarge, 0)
	}
	if !utf8.Valid(text) {
		return nil, NewError(CodeTextNotUTF8, 0)
	}
	return text, nil
}
