package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// lang is a language the pages are shown in.
type lang string

const (
	langEN lang = "en"
	langID lang = "id"

	// langCookieName holds the language a visitor chose with the switch.
	langCookieName = "answerbox_lang"
)

// parseLang accepts exactly "en" and "id".
func parseLang(v string) (lang, bool) {
	switch l := lang(v); l {
	case langEN, langID:
		return l, true
	}
	return "", false
}

// requestLang is the language of a request: the language cookie when it
// holds a known value, else Indonesian when Accept-Language prefers it over
// English, else English. Nothing about it is stored on the server.
func requestLang(r *http.Request) lang {
	if c, err := r.Cookie(langCookieName); err == nil {
		if l, ok := parseLang(c.Value); ok {
			return l
		}
	}
	if preferIndonesian(r.Header.Get("Accept-Language")) {
		return langID
	}
	return langEN
}

// preferIndonesian reports whether an Accept-Language header ranks
// Indonesian above English: its best q is higher, or equal and above 0 with
// the first Indonesian range before the first English one. A range without
// q has q 1; a q that is not a number from 0 to 1, or is 0, drops the range.
// Indonesian is the primary subtag "id" or the retired "in"; any other
// language, and "*", is ignored.
func preferIndonesian(header string) bool {
	bestID, bestEN := 0.0, 0.0
	firstID, firstEN := -1, -1
	for i, part := range strings.Split(header, ",") {
		tag, params, _ := strings.Cut(part, ";")
		tag = strings.TrimSpace(tag)
		q, ok := rangeQ(params)
		if tag == "" || !ok {
			continue
		}
		primary, _, _ := strings.Cut(tag, "-")
		switch strings.ToLower(primary) {
		case "id", "in":
			bestID = max(bestID, q)
			if firstID < 0 {
				firstID = i
			}
		case "en":
			bestEN = max(bestEN, q)
			if firstEN < 0 {
				firstEN = i
			}
		}
	}
	switch {
	case bestID > bestEN:
		return true
	case bestID == bestEN && bestID > 0:
		return firstID < firstEN
	}
	return false
}

// rangeQ reads the q of a language range's parameters; ok is false when the
// range is to be dropped.
func rangeQ(params string) (q float64, ok bool) {
	q = 1
	for p := range strings.SplitSeq(params, ";") {
		name, value, found := strings.Cut(p, "=")
		if !found || !strings.EqualFold(strings.TrimSpace(name), "q") {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || !(v > 0 && v <= 1) {
			return 0, false
		}
		q = v
	}
	return q, true
}

// langCookieMaxAge keeps the chosen language for a year.
const langCookieMaxAge = 365 * 24 * 60 * 60

// newLangCookie is the cookie the switch sets for l.
func newLangCookie(l lang) *http.Cookie {
	return &http.Cookie{
		Name:     langCookieName,
		Value:    string(l),
		Path:     "/",
		MaxAge:   langCookieMaxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}

// setLanguage is POST /lang: it sets the language cookie and sends the
// visitor back to the page the switch was on. It reads nothing else of the
// request and changes nothing else.
func (s *server) setLanguage(w http.ResponseWriter, r *http.Request) {
	if !s.parseStudentForm(w, r) {
		return
	}
	l, ok := parseLang(r.PostFormValue("lang"))
	if !ok {
		s.errorPage(w, r, http.StatusBadRequest)
		return
	}
	http.SetCookie(w, newLangCookie(l))
	http.Redirect(w, r, safeReturn(r.PostFormValue("next")), http.StatusSeeOther)
}

// safeReturn returns next when it is a path on this site, else "/": it
// starts with one "/" (not "//" or "/\"), holds no "\" and no control byte,
// and parses without a scheme, host or user.
func safeReturn(next string) string {
	if next == "" || next[0] != '/' || (len(next) > 1 && (next[1] == '/' || next[1] == '\\')) {
		return "/"
	}
	for i := range len(next) {
		if c := next[i]; c == '\\' || c < 0x20 || c == 0x7f {
			return "/"
		}
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return "/"
	}
	return next
}

// returner is page data rendered by a POST on a path that has no GET page
// of its own; returnPath is the GET page that shows the same thing.
type returner interface {
	returnPath() string
}

// pageReturn is where the switch on a page sends the visitor back to: the
// page itself for GET and HEAD, else the data's return path, else the
// request path.
func pageReturn(r *http.Request, data any) string {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return safeReturn(r.URL.RequestURI())
	}
	if d, ok := data.(returner); ok {
		if p := d.returnPath(); p != "" {
			return safeReturn(p)
		}
	}
	return safeReturn(r.URL.EscapedPath())
}

func (d examDetailData) returnPath() string { return examPath(d.Exam.ID) }

func (d attemptDetailData) returnPath() string { return attemptPath(d.Exam.ID, d.Attempt.ID) }

func (d editorData) returnPath() string { return editPath(d.Exam.ID) }

func (d landingData) returnPath() string {
	if d.ExamTitle != "" {
		return "/?code=" + url.QueryEscape(d.Code)
	}
	return "/"
}

func (d errorData) returnPath() string {
	if d.Teacher {
		return "/admin/exams"
	}
	return "/"
}
