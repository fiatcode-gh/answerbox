package web

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// scriptWordsAllowed are string literals that look like words but are not
// text a person reads. Each entry carries its reason.
var scriptWordsAllowed = map[string]string{}

// jsStrings returns the string literals of a script, unquoted only as far as
// the checks need: comments are skipped, escapes are kept as written. Regular
// expression literals are not recognised; the scripts hold none with quotes.
func jsStrings(src string) []string {
	var out []string
	for i := 0; i < len(src); i++ {
		switch c := src[i]; {
		case strings.HasPrefix(src[i:], "//"):
			end := strings.IndexByte(src[i:], '\n')
			if end < 0 {
				return out
			}
			i += end
		case strings.HasPrefix(src[i:], "/*"):
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return out
			}
			i += end + 3
		case c == '"' || c == '\'' || c == '`':
			var b strings.Builder
			j := i + 1
			for ; j < len(src) && src[j] != c; j++ {
				if src[j] == '\\' && j+1 < len(src) {
					b.WriteByte(src[j])
					j++
				}
				b.WriteByte(src[j])
			}
			out = append(out, b.String())
			i = j
		}
	}
	return out
}

// scriptWordProblems lists the literals of src that hold text for a person:
// two words in a row, or one of the characters only text uses. Selectors,
// media queries and header values hold "[", "=", "/" or ":" and are not text.
func scriptWordProblems(name, src string) []string {
	var problems []string
	for _, lit := range jsStrings(src) {
		switch {
		case strings.ContainsAny(lit, "…✓✗") || strings.Contains(lit, `\u2026`) || strings.Contains(lit, `\u2713`) || strings.Contains(lit, `\u2717`):
			problems = append(problems, name+": text character in "+strconv.Quote(lit))
		case lit == "use strict", strings.ContainsAny(lit, "[=/:"):
		case scriptWordsAllowed[lit] != "":
		case twoWords.MatchString(lit):
			problems = append(problems, name+": words in "+strconv.Quote(lit))
		}
	}
	return problems
}

// twoWords is two words joined by a space.
var twoWords = regexp.MustCompile(`[A-Za-z]{2,} [A-Za-z]{2,}`)

func TestScripts_HoldNoWords(t *testing.T) {
	sample := scriptWordProblems("x.js", `
		"use strict"; // a comment's "quoted words" are not literals
		/* nor "these ones" */
		var a = "Not saved";
		var b = '✓ done';
		var c = "\u2026";
		var d = document.querySelector("a.bubble[href=\"#q1\"] span");
		var e = "(max-width: 991.98px)";
		var f = "application/x-www-form-urlencoded";
		var g = "saved";
		var h = `+"`Saving it`"+`;
	`)
	want := []string{
		`x.js: words in "Not saved"`, `x.js: text character in "✓ done"`, `x.js: text character in "\\u2026"`,
		"x.js: words in \"Saving it\"",
	}
	requireProblems(t, "bad sample", sample, want...)
	if len(sample) != len(want) {
		t.Errorf("bad sample: %d problems, want %d: %q", len(sample), len(want), sample)
	}
	for _, name := range []string{"exam.js", "entry.js", "results.js", "copy.js"} {
		requireProblems(t, name, scriptWordProblems(name, string(servedStatic(t, name))))
	}
}
