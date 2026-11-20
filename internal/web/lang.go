package web

import (
	"net/http"
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
