package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

//go:embed templates
var templateFS embed.FS

const baseTemplate = "base.html"

// partials only define named templates shared by pages; they are parsed into
// every page set and are not pages themselves.
var partials = []string{"admin_header.html", "brand.html", "admin_questions.html", "admin_editor_fields.html", "section_head.html"}

// renderer holds one template set per language and page: base.html and the
// partials plus that page file, parsed once at startup with the template
// functions bound to the language.
type renderer struct {
	pages map[lang]map[string]*template.Template
}

// layoutData is what base.html executes with; its blocks hand Page to the
// page's templates.
type layoutData struct {
	Page any
	// Return is where the language switch sends the visitor back to.
	Return string
}

func newRenderer(a assets) (*renderer, error) {
	r := &renderer{pages: make(map[lang]map[string]*template.Template)}
	for _, l := range []lang{langEN, langID} {
		pages, err := parsePages(a, l)
		if err != nil {
			return nil, err
		}
		r.pages[l] = pages
	}
	return r, nil
}

// parsePages parses every page with the template functions of l.
func parsePages(a assets, l lang) (map[string]*template.Template, error) {
	shared := append([]string{baseTemplate}, partials...)
	for i, name := range shared {
		shared[i] = "templates/" + name
	}
	funcs := langFuncs(l)
	funcs["static"] = a.url
	// chars is the code as one string per symbol, for its tiles.
	funcs["chars"] = func(s string) []string { return strings.Split(s, "") }
	base, err := template.New(baseTemplate).Funcs(funcs).ParseFS(templateFS, shared...)
	if err != nil {
		return nil, fmt.Errorf("web: parse %s and partials: %w", baseTemplate, err)
	}
	entries, err := fs.ReadDir(templateFS, "templates")
	if err != nil {
		return nil, err
	}
	pages := make(map[string]*template.Template)
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
		pages[e.Name()] = t
	}
	return pages, nil
}

// render executes page in language l into a buffer and writes it only when
// execution succeeded, so a template error never produces a half-written
// page.
func (r *renderer) render(w http.ResponseWriter, status int, l lang, page string, data layoutData) error {
	t, ok := r.pages[l][page]
	if !ok {
		return fmt.Errorf("web: unknown page %q in %q", page, l)
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

// page renders a page in the request's language, falling back to a plain
// 500 when rendering fails.
func (s *server) page(w http.ResponseWriter, r *http.Request, status int, page string, data any) {
	if err := s.pages.render(w, status, requestLang(r), page, layoutData{Page: data, Return: pageReturn(r, data)}); err != nil {
		s.log.Error("render failed", "path", r.URL.Path, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

type errorData struct {
	Status         int
	Title, Message string
	// Teacher offers the teacher pages as a way back; only errors under
	// /admin do.
	Teacher bool
}

// errorPage renders error.html for status with its standard message.
func (s *server) errorPage(w http.ResponseWriter, r *http.Request, status int) {
	s.errorPageMessage(w, r, status, "")
}

// errorPageMessage renders error.html for status in the request's language;
// a non-empty message replaces the standard one. A status without texts of
// its own keeps its standard title and gets the 400 message.
func (s *server) errorPageMessage(w http.ResponseWriter, r *http.Request, status int, message string) {
	l := loc(r)
	data := errorData{Title: http.StatusText(status), Message: l.T("error.400.message")}
	switch status {
	case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusRequestEntityTooLarge, http.StatusInternalServerError:
		key := "error." + strconv.Itoa(status)
		data.Title, data.Message = l.T(key+".title"), l.T(key+".message")
	}
	if message != "" {
		data.Message = message
	}
	data.Status = status
	path := r.URL.EscapedPath()
	data.Teacher = path == "/admin" || strings.HasPrefix(path, "/admin/")
	s.page(w, r, status, "error.html", data)
}
