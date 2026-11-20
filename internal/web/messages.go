package web

import (
	"fmt"
	"html/template"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// bundle is one area's catalogue texts in both languages, keyed by stable
// identifiers ("area.name"). Each key lives in exactly one bundle.
type bundle struct{ en, id map[string]string }

var bundles = []bundle{coreMessages, studentMessages, examsMessages, resultsMessages, editorMessages}

// catalogue is every bundle merged per language, built once.
var catalogue = mergeBundles(bundles)

func mergeBundles(bs []bundle) map[lang]map[string]string {
	out := map[lang]map[string]string{langEN: {}, langID: {}}
	for _, b := range bs {
		for k, v := range b.en {
			out[langEN][k] = v
		}
		for k, v := range b.id {
			out[langID][k] = v
		}
	}
	return out
}

// lookup is the catalogue text of key in l.
func lookup(l lang, key string) (string, bool) {
	text, ok := catalogue[l][key]
	return text, ok
}

// localizer words what handlers say in one language.
type localizer struct{ lang lang }

// loc is the localizer of the request's language.
func loc(r *http.Request) localizer { return localizer{requestLang(r)} }

// T is the text of key: verbatim without args, else formatted with them.
// An unknown key comes back as itself.
func (l localizer) T(key string, args ...any) string {
	text, ok := lookup(l.lang, key)
	if !ok {
		return key
	}
	if len(args) == 0 {
		return text
	}
	return fmt.Sprintf(text, args...)
}

// N is the plural text of key for n: key+".one" when n is 1, else
// key+".other", formatted with n followed by args.
func (l localizer) N(key string, n int, args ...any) string {
	key = pluralKey(key, n)
	text, ok := lookup(l.lang, key)
	if !ok {
		return key
	}
	return fmt.Sprintf(text, append([]any{n}, args...)...)
}

func pluralKey(key string, n int) string {
	if n == 1 {
		return key + ".one"
	}
	return key + ".other"
}

// Date is t as "Mon, 2 Jan 2006" with the weekday and month of l.
func (l localizer) Date(t time.Time) string {
	return l.T("date.weekday."+strconv.Itoa(int(t.Weekday()))) + ", " + l.Day(t) + " " + strconv.Itoa(t.Year())
}

// Day is t as "2 Jan" with the month of l.
func (l localizer) Day(t time.Time) string {
	return strconv.Itoa(t.Day()) + " " + l.T("date.month."+strconv.Itoa(int(t.Month())))
}

// markupEscaper escapes a catalogue text for t and tn: it is source code, so
// only what would read as markup is escaped and its quotes keep their bytes.
var markupEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// argEscaper escapes a template argument the way html/template escapes text.
var argEscaper = strings.NewReplacer(
	"\x00", "\uFFFD", `"`, "&#34;", "&", "&amp;", "'", "&#39;", "+", "&#43;", "<", "&lt;", ">", "&gt;",
)

// escapeArgs makes the arguments of t, tn and th safe to put into markup:
// integers pass through, anything else is text and is escaped.
func escapeArgs(args []any) []any {
	out := make([]any, len(args))
	for i, a := range args {
		v := reflect.ValueOf(a)
		switch {
		case a == nil:
			out[i] = argEscaper.Replace(fmt.Sprint(a))
		case v.CanInt() || v.CanUint():
			out[i] = a
		case v.Kind() == reflect.String:
			out[i] = argEscaper.Replace(v.String())
		default:
			out[i] = argEscaper.Replace(fmt.Sprint(a))
		}
	}
	return out
}

// formatMarkup formats text, already safe markup, with escaped args.
func formatMarkup(text string, args []any) template.HTML {
	if len(args) == 0 {
		return template.HTML(text)
	}
	return template.HTML(fmt.Sprintf(text, escapeArgs(args)...))
}

// langFuncs are the template functions bound to one language: t, tn and th
// word a page; lang names its language.
func langFuncs(l lang) template.FuncMap {
	plain := func(fn, key string) (string, error) {
		if strings.HasSuffix(key, "_html") {
			return "", fmt.Errorf("%s %q: an _html key needs th", fn, key)
		}
		text, ok := lookup(l, key)
		if !ok {
			return "", fmt.Errorf("%s %q: no such key", fn, key)
		}
		return markupEscaper.Replace(text), nil
	}
	return template.FuncMap{
		"t": func(key string, args ...any) (template.HTML, error) {
			text, err := plain("t", key)
			if err != nil {
				return "", err
			}
			return formatMarkup(text, args), nil
		},
		"tn": func(key string, n int, args ...any) (template.HTML, error) {
			text, err := plain("tn", pluralKey(key, n))
			if err != nil {
				return "", err
			}
			return formatMarkup(text, append([]any{n}, args...)), nil
		},
		"th": func(key string, args ...any) (template.HTML, error) {
			if !strings.HasSuffix(key, "_html") {
				return "", fmt.Errorf("th %q: not an _html key", key)
			}
			text, ok := lookup(l, key)
			if !ok {
				return "", fmt.Errorf("th %q: no such key", key)
			}
			return formatMarkup(text, args), nil
		},
		"lang": func() string { return string(l) },
	}
}
