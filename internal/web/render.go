package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"slices"
	"strings"
)

//go:embed templates
var templateFS embed.FS

const baseTemplate = "base.html"

// partials only define named templates shared by pages; they are parsed into
// every page set and are not pages themselves.
var partials = []string{"admin_header.html", "brand.html", "admin_questions.html", "admin_editor_fields.html", "section_head.html"}

// renderer holds one template set per page: base.html and the partials plus
// that page file, parsed once at startup.
type renderer struct {
	pages map[string]*template.Template
}

func newRenderer(a assets) (*renderer, error) {
	shared := append([]string{baseTemplate}, partials...)
	for i, name := range shared {
		shared[i] = "templates/" + name
	}
	base, err := template.New(baseTemplate).
		Funcs(template.FuncMap{
			"static": a.url,
			// chars is the code as one string per symbol, for its tiles.
			"chars": func(s string) []string { return strings.Split(s, "") },
		}).
		ParseFS(templateFS, shared...)
	if err != nil {
		return nil, fmt.Errorf("web: parse %s and partials: %w", baseTemplate, err)
	}
	entries, err := fs.ReadDir(templateFS, "templates")
	if err != nil {
		return nil, err
	}
	r := &renderer{pages: make(map[string]*template.Template)}
	for _, e := range entries {
		if e.IsDir() || e.Name() == baseTemplate || slices.Contains(partials, e.Name()) {
			continue
		}
		t, err := base.Clone()
		if err != nil {
			return nil, err
		}
		if _, err := t.ParseFS(templateFS, "templates/"+e.Name()); err != nil {
			return nil, fmt.Errorf("web: parse %s: %w", e.Name(), err)
		}
		r.pages[e.Name()] = t
	}
	return r, nil
}

// render executes page into a buffer and writes it only when execution
// succeeded, so a template error never produces a half-written page.
func (r *renderer) render(w http.ResponseWriter, status int, page string, data any) error {
	t, ok := r.pages[page]
	if !ok {
		return fmt.Errorf("web: unknown page %q", page)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, baseTemplate, data); err != nil {
		return fmt.Errorf("web: render %s: %w", page, err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, err := w.Write(buf.Bytes())
	return err
}

// page renders a page, falling back to a plain 500 when rendering fails.
func (s *server) page(w http.ResponseWriter, r *http.Request, status int, page string, data any) {
	if err := s.pages.render(w, status, page, data); err != nil {
		s.log.Error("render failed", "path", r.URL.Path, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

type errorData struct {
	Status         int
	Title, Message string
}

var errorMessages = map[int]errorData{
	http.StatusForbidden:             {Title: "Not allowed", Message: "This request was refused."},
	http.StatusNotFound:              {Title: "Page not found", Message: "There is nothing at this address."},
	http.StatusRequestEntityTooLarge: {Title: "Upload too large", Message: "Upload is larger than 10 MB."},
	http.StatusInternalServerError:   {Title: "Something went wrong", Message: "The server could not finish this request. Please try again."},
}

// errorPage renders error.html for status with its standard message.
func (s *server) errorPage(w http.ResponseWriter, r *http.Request, status int) {
	s.errorPageMessage(w, r, status, "")
}

// errorPageMessage renders error.html for status; a non-empty message
// replaces the standard one.
func (s *server) errorPageMessage(w http.ResponseWriter, r *http.Request, status int, message string) {
	data, ok := errorMessages[status]
	if !ok {
		data = errorData{Title: http.StatusText(status), Message: "The request could not be completed."}
	}
	if message != "" {
		data.Message = message
	}
	data.Status = status
	s.page(w, r, status, "error.html", data)
}
