package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

//go:embed static
var staticFS embed.FS

type asset struct {
	data        []byte
	version     string // first 8 hex chars of the served content's SHA-256
	contentType string
}

// assets maps a path relative to static/ to its embedded file.
type assets map[string]asset

// loadAssets indexes every embedded file. Stylesheets come last: their
// url("…") references are versioned against the files indexed before them,
// and their own version is the hash of the rewritten bytes, so a new font
// gives a new stylesheet URL.
func loadAssets() (assets, error) {
	var files, stylesheets []string
	err := fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if path.Ext(p) == ".css" {
			stylesheets = append(stylesheets, p)
		} else {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("web: index static assets: %w", err)
	}
	a := assets{}
	for _, p := range append(files, stylesheets...) {
		data, err := fs.ReadFile(staticFS, p)
		if err != nil {
			return nil, fmt.Errorf("web: index static assets: %w", err)
		}
		if path.Ext(p) == ".css" {
			if data, err = versionCSS(data, a); err != nil {
				return nil, fmt.Errorf("web: index static assets: %s: %w", p, err)
			}
		}
		sum := sha256.Sum256(data)
		ct := mime.TypeByExtension(path.Ext(p))
		if ct == "" {
			ct = http.DetectContentType(data)
		}
		a[strings.TrimPrefix(p, "static/")] = asset{data: data, version: hex.EncodeToString(sum[:])[:8], contentType: ct}
	}
	return a, nil
}

// cssURL is a double-quoted url() whose path has no scheme (so not data:).
var cssURL = regexp.MustCompile(`url\("([^":)]+)"\)`)

// versionCSS replaces each url("PATH") of a stylesheet, PATH relative to
// static/, by the versioned URL of that asset. A PATH that names no asset is
// an error, as an unknown name is for the template function.
func versionCSS(css []byte, a assets) ([]byte, error) {
	var err error
	out := cssURL.ReplaceAllFunc(css, func(m []byte) []byte {
		u, uerr := a.url(string(cssURL.FindSubmatch(m)[1]))
		if uerr != nil {
			err = uerr
			return m
		}
		return []byte(`url("` + u + `")`)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// url is the template func "static": the versioned URL of an embedded asset.
func (a assets) url(name string) (string, error) {
	f, ok := a[name]
	if !ok {
		return "", fmt.Errorf("static: unknown asset %q", name)
	}
	return "/static/" + name + "?v=" + f.version, nil
}

func (s *server) serveStatic(w http.ResponseWriter, r *http.Request) {
	f, ok := s.assets[r.PathValue("path")]
	if !ok {
		s.notFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Type", f.contentType)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(f.data))
}
