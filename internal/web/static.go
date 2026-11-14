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
	"strings"
	"time"
)

//go:embed static
var staticFS embed.FS

type asset struct {
	data        []byte
	version     string // first 8 hex chars of the content SHA-256
	contentType string
}

// assets maps a path relative to static/ to its embedded file.
type assets map[string]asset

func loadAssets() (assets, error) {
	a := assets{}
	err := fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(staticFS, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		ct := mime.TypeByExtension(path.Ext(p))
		if ct == "" {
			ct = http.DetectContentType(data)
		}
		a[strings.TrimPrefix(p, "static/")] = asset{data: data, version: hex.EncodeToString(sum[:])[:8], contentType: ct}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("web: index static assets: %w", err)
	}
	return a, nil
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
