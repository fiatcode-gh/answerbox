package web

import (
	"io/fs"
	"net/http"
	"path"
	"regexp"
)

var (
	mediaKeyRe  = regexp.MustCompile(`^[0-9a-f]{32}$`)
	mediaNameRe = regexp.MustCompile(`^[0-9a-f]{32}\.(png|jpg|gif|webp)$`)
)

var mediaTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// serveMedia serves an exam image. Both path values are validated before
// the filesystem is touched, so a request can only name a file the importer
// wrote; only regular files are served.
func (s *server) serveMedia(w http.ResponseWriter, r *http.Request) {
	key, name := r.PathValue("key"), r.PathValue("name")
	if !mediaKeyRe.MatchString(key) || !mediaNameRe.MatchString(name) {
		s.notFound(w, r)
		return
	}
	media := s.deps.Exams.MediaFS()
	p := key + "/" + name
	if info, err := fs.Stat(media, p); err != nil || !info.Mode().IsRegular() {
		s.notFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mediaTypes[path.Ext(name)])
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFileFS(w, r, media, p)
}
