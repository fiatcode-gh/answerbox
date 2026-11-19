package web

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// goldenPages are the page goldens of TestUnsectionedPages_Unchanged.
var goldenPages = []string{"student-exam", "student-result", "teacher-exam", "teacher-revision", "teacher-attempts", "teacher-attempt"}

// pageFacts returns the facts of one page golden as text lines in page
// order: question and option order, picked and correct state, scores, section
// structure and the other values the page pins. It lets the UI revamp change
// the markup around those facts while TestUnsectionedPages_Unchanged still
// proves the pages show the same exam, answers and scores as release 293ab19.
// name is the golden's name; an unknown name panics.
func pageFacts(name, html string) []string {
	facts := []string{"sections=" + strconv.Itoa(len(factClassRe("section-head").FindAllStringIndex(html, -1)))}
	add := func(format string, args ...any) { facts = append(facts, fmt.Sprintf(format, args...)) }
	main := factOpenTag(html, "main")
	switch name {
	case "student-exam":
		add("main remaining-ms=%s monitor=%s", factAttr(main, "data-remaining-ms"), factAttr(main, "data-monitor"))
		timer := factFirst(html, "span", `\bid="timer"`)
		add("timer %s %s", factAttr(timer.open, "data-level"), factText(timer.inner))
		add("answered %s", factText(factFirst(html, `\w+`, `\bid="answered-count"`).inner))
		for _, a := range factElements(html, "a", factClassRe("bubble").String()) {
			add("nav %s %s %s", strings.TrimPrefix(factAttr(a.open, "href"), "#q"), factAttr(a.open, "data-answered"), factText(a.inner))
		}
		for _, q := range factElements(html, "section", factClassRe("question").String()) {
			add("question %s id=%s", strings.TrimPrefix(factAttr(q.open, "id"), "q"), factAttr(q.open, "data-question-id"))
			add("eyebrow %s", factText(factFirst(q.inner, "p", factClassRe("eyebrow").String()).inner))
			add("state %s", factAttr(factFirst(q.inner, "span", factClassRe("q-state").String()).open, "data-state"))
			add("body %s", factHTML(factFirst(q.inner, "div", factClassRe("question-body").String()).inner))
			for _, o := range factElements(q.inner, "label", factClassRe("option").String()) {
				radio := factOpenTagWith(o.inner, "input", `\btype="radio"`)
				add("option %s value=%s picked=%t %s",
					factText(factFirst(o.inner, "span", factClassRe("bubble").String()).inner),
					factAttr(radio, "value"),
					regexp.MustCompile(`\schecked[\s>/]`).MatchString(radio),
					factHTML(factFirst(o.inner, "span", factClassRe("option-body").String()).inner))
			}
		}
	case "student-result":
		add("show %s status %s", factAttr(main, "data-show"), factAttr(main, "data-status"))
		add("h1 %s", factText(factFirst(html, "h1", "").inner))
		add("score %s", factText(factFirst(html, `\w+`, `\bid="score"`).inner))
		for _, q := range factElements(html, "section", factClassRe("review-question").String()) {
			add("question %s", factText(factFirst(q.inner, "p", factClassRe("eyebrow").String()).inner))
			add("body %s", factHTML(factFirst(q.inner, "div", factClassRe("question-body").String()).inner))
			add("no-answer %t", factClassRe("no-answer").MatchString(q.inner))
			for _, o := range factElements(q.inner, "li", factClassRe("review-option").String()) {
				add("option %s picked=%s correct=%s %s | %s",
					factText(factFirst(o.inner, "span", factClassRe("bubble").String()).inner),
					factAttr(o.open, "data-picked"), factAttr(o.open, "data-correct"),
					factHTML(factFirst(o.inner, "span", factClassRe("option-body").String()).inner),
					factText(factFirst(o.inner, "span", factClassRe("review-mark").String()).inner))
			}
		}
	case "teacher-exam", "teacher-revision":
		for _, h := range factElements(html, "h1", "") {
			add("h1 %s", factText(h.inner))
		}
		for _, m := range factPairRe.FindAllStringSubmatch(html, -1) {
			add("setting %s = %s", factText(m[1]), factText(m[2]))
		}
		if name == "teacher-exam" {
			add("code %s", factText(factFirst(html, `\w+`, `\bid="exam-code"`).inner))
			add("link %s", factAttr(factFirst(html, `\w+`, `\bid="student-link"`).open, "href"))
			head := factFirst(html, "div", factClassRe("exam-head").String())
			add("badge %s", factText(factFirst(head.inner, `\w+`, factClassRe("badge").String()).inner))
		}
		for _, q := range factElements(html, "section", factClassRe("preview-question").String()) {
			add("question %s", strings.TrimPrefix(factAttr(q.open, "id"), "q"))
			add("body %s", factHTML(factFirst(q.inner, "div", factClassRe("question-body").String()).inner))
			for _, o := range factElements(q.inner, "li", `\bdata-correct="`) {
				add("option correct=%s %s | %s", factAttr(o.open, "data-correct"),
					factHTML(factFirst(o.inner, "div", factClassRe("option-body").String()).inner),
					factText(factFirst(o.inner, "span", factClassRe("correct-mark").String()).inner))
			}
		}
	case "teacher-attempts":
		add("counts %s", factText(factFirst(html, `\w+`, factClassRe("counts").String()).inner))
		table := factFirst(html, "table", `\bid="attempts"`)
		for _, th := range factElements(table.inner, "th", "") {
			add("header %s", factText(th.inner))
		}
		for _, tr := range factElements(table.inner, "tr", `\bdata-attempt-id="`) {
			add("attempt %s", factAttr(tr.open, "data-attempt-id"))
			for _, td := range factElements(tr.inner, "td", "") {
				add("cell %s", factText(td.inner))
			}
			for _, a := range factElements(tr.inner, "a", "") {
				add("link %s", factAttr(a.open, "href"))
			}
		}
	case "teacher-attempt":
		add("h1 %s", factText(factFirst(html, "h1", "").inner))
		head := factFirst(html, "div", factClassRe("exam-head").String())
		add("badge %s", factText(factFirst(head.inner, `\w+`, factClassRe("badge").String()).inner))
		summary := factFirst(html, "dl", factClassRe("attempt-summary").String())
		for _, m := range factPairRe.FindAllStringSubmatch(summary.inner, -1) {
			add("summary %s = %s", factText(m[1]), factText(m[2]))
		}
		for _, tr := range factElements(html, "tr", factClassRe("event").String()) {
			var cells []string
			for _, td := range factElements(tr.inner, "td", "") {
				cells = append(cells, factText(td.inner))
			}
			add("event %s", strings.Join(cells, " | "))
		}
		for _, q := range factElements(html, "section", factClassRe("detail-question").String()) {
			add("question %s", strings.TrimPrefix(factAttr(q.open, "id"), "q"))
			add("eyebrow %s", factText(factFirst(q.inner, "p", factClassRe("eyebrow").String()).inner))
			add("body %s", factHTML(factFirst(q.inner, "div", factClassRe("question-body").String()).inner))
			for _, o := range factElements(q.inner, "li", `\bdata-correct="`) {
				add("option correct=%s picked=%s %s | %s", factAttr(o.open, "data-correct"), factAttr(o.open, "data-picked"),
					factHTML(factFirst(o.inner, "div", factClassRe("option-body").String()).inner),
					factText(factFirst(o.inner, "p", factClassRe("option-marks").String()).inner))
			}
		}
	default:
		panic("pageFacts: unknown page " + name)
	}
	return facts
}

var (
	factTagRe   = regexp.MustCompile(`<[^>]*>`)
	factSpaceRe = regexp.MustCompile(`\s+`)
	factPairRe  = regexp.MustCompile(`(?s)<dt\b[^>]*>(.*?)</dt>\s*<dd\b[^>]*>(.*?)</dd>`)
)

// factText is the text of an HTML fragment: tags removed, entities kept,
// white space runs made one space, trimmed.
func factText(s string) string {
	return strings.TrimSpace(factSpaceRe.ReplaceAllString(factTagRe.ReplaceAllString(s, ""), " "))
}

// factHTML is the inner HTML of an element, trimmed.
func factHTML(s string) string { return strings.TrimSpace(s) }

// factClassRe matches a class attribute that holds the class name among others.
func factClassRe(name string) *regexp.Regexp {
	return regexp.MustCompile(`\bclass="(?:[^"]*\s)?` + regexp.QuoteMeta(name) + `(?:\s[^"]*)?"`)
}

// factAttr is the value of one attribute of an opening tag, empty when absent.
func factAttr(openTag, name string) string {
	m := regexp.MustCompile(`\s` + regexp.QuoteMeta(name) + `="([^"]*)"`).FindStringSubmatch(openTag)
	if m == nil {
		return ""
	}
	return m[1]
}

// factOpenTag is the first opening tag of the element, empty when absent.
func factOpenTag(s, tag string) string { return factOpenTagWith(s, tag, "") }

// factOpenTagWith is the first opening tag of the element whose tag matches
// attrs, empty when absent.
func factOpenTagWith(s, tag, attrs string) string {
	attrRe := regexp.MustCompile(attrs)
	for _, m := range regexp.MustCompile(`<`+tag+`\b[^>]*>`).FindAllString(s, -1) {
		if attrRe.MatchString(m) {
			return m
		}
	}
	return ""
}

type factElement struct{ open, inner string }

// factElements returns, in page order, every element whose tag name matches
// tag and whose opening tag matches attrs, with its inner HTML up to the
// balancing closing tag.
func factElements(s, tag, attrs string) []factElement {
	attrRe := regexp.MustCompile(attrs)
	var out []factElement
	for _, loc := range regexp.MustCompile(`<(`+tag+`)\b[^>]*>`).FindAllStringSubmatchIndex(s, -1) {
		open := s[loc[0]:loc[1]]
		if !attrRe.MatchString(open) {
			continue
		}
		name := s[loc[2]:loc[3]]
		inner, ok := factBalanced(s[loc[1]:], name)
		if !ok {
			continue
		}
		out = append(out, factElement{open, inner})
	}
	return out
}

// factFirst is the first element factElements finds, zero when none.
func factFirst(s, tag, attrs string) factElement {
	if all := factElements(s, tag, attrs); len(all) > 0 {
		return all[0]
	}
	return factElement{}
}

// factBalanced returns s up to the closing tag of name that balances an
// opening tag just before s.
func factBalanced(s, name string) (string, bool) {
	re := regexp.MustCompile(`(?i)<(/?)` + regexp.QuoteMeta(name) + `\b[^>]*>`)
	depth := 1
	for _, loc := range re.FindAllStringSubmatchIndex(s, -1) {
		if loc[3] > loc[2] {
			depth--
		} else {
			depth++
		}
		if depth == 0 {
			return s[:loc[0]], true
		}
	}
	return "", false
}

// readGoldenPage reads one page golden of testdata/unsectioned.
func readGoldenPage(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "unsectioned", name+".golden"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// swapFirstTwo swaps the first two blocks that start with open and end with
// the first close after it. The blocks must be adjacent apart from white space.
func swapFirstTwo(t *testing.T, s, open, close string) string {
	t.Helper()
	block := func(from int) (int, int) {
		a := strings.Index(s[from:], open)
		if a < 0 {
			t.Fatalf("no block %q after %d", open, from)
		}
		a += from
		b := strings.Index(s[a:], close)
		if b < 0 {
			t.Fatalf("block %q at %d is not closed by %q", open, a, close)
		}
		return a, a + b + len(close)
	}
	a1, b1 := block(0)
	a2, b2 := block(b1)
	if strings.TrimSpace(s[b1:a2]) != "" {
		t.Fatalf("blocks %q are not adjacent: %q between them", open, s[b1:a2])
	}
	return s[:a1] + s[a2:b2] + s[b1:a2] + s[a1:b1] + s[b2:]
}

func replaceOnce(t *testing.T, s, old, new string) string {
	t.Helper()
	if !strings.Contains(s, old) {
		t.Fatalf("%q not found", old)
	}
	return strings.Replace(s, old, new, 1)
}

func countFacts(facts []string, prefix string) int {
	n := 0
	for _, f := range facts {
		if strings.HasPrefix(f, prefix) {
			n++
		}
	}
	return n
}

// TestGoldenFacts_Sensitive proves that pageFacts reads the facts the page
// goldens pin from the 293ab19 files, that a change of rendered order, pick,
// correctness, score or section structure changes them, and that a change of
// markup around them does not.
func TestGoldenFacts_Sensitive(t *testing.T) {
	counts := []struct {
		page   string
		prefix string
		want   int
	}{
		{"student-exam", "question ", 5},
		{"student-exam", "option ", 15},
		{"student-exam", "nav ", 5},
		{"student-result", "question ", 5},
		{"student-result", "option ", 15},
		{"student-result", "score 0 / 5", 1},
		{"teacher-exam", "question ", 5},
		{"teacher-exam", "option ", 15},
		{"teacher-exam", "option correct=true ", 5},
		{"teacher-revision", "question ", 5},
		{"teacher-revision", "option ", 15},
		{"teacher-revision", "option correct=true ", 5},
		{"teacher-attempts", "attempt ", 2},
		{"teacher-attempts", "header ", 9},
		{"teacher-attempt", "question ", 5},
		{"teacher-attempt", "option ", 15},
		{"teacher-attempt", "event ", 1},
	}
	for _, c := range counts {
		facts := pageFacts(c.page, readGoldenPage(t, c.page))
		if got := countFacts(facts, c.prefix); got != c.want {
			t.Errorf("%s: %d facts %q, want %d; facts:\n%s", c.page, got, c.prefix, c.want, strings.Join(facts, "\n"))
		}
	}
	for _, page := range goldenPages {
		facts := pageFacts(page, readGoldenPage(t, page))
		if len(facts) == 0 || facts[0] != "sections=0" {
			t.Errorf("%s: first fact %q, want sections=0", page, facts)
		}
	}

	mutations := []struct {
		name   string
		page   string
		mutate func(t *testing.T, s string) string
	}{
		{"option order", "student-exam", func(t *testing.T, s string) string {
			return swapFirstTwo(t, s, `<label class="option"`, `</label>`)
		}},
		{"radio checked", "student-exam", func(t *testing.T, s string) string {
			return replaceOnce(t, s, `id="o8">`, `id="o8" checked>`)
		}},
		{"question order", "student-exam", func(t *testing.T, s string) string {
			return swapFirstTwo(t, s, `<section class="question"`, `</section>`)
		}},
		{"section head", "student-exam", func(t *testing.T, s string) string {
			return replaceOnce(t, s, `<section class="question"`, `<div class="section-head"><h2 class="section-title">X</h2></div><section class="question"`)
		}},
		{"review pick", "student-result", func(t *testing.T, s string) string {
			return replaceOnce(t, s, `data-picked="true"`, `data-picked="false"`)
		}},
		{"result score", "student-result", func(t *testing.T, s string) string {
			return replaceOnce(t, s, `<p id="score">0 / 5</p>`, `<p id="score">1 / 5</p>`)
		}},
		{"preview correct", "teacher-exam", func(t *testing.T, s string) string {
			return replaceOnce(t, s, `data-correct="true"`, `data-correct="false"`)
		}},
		{"row score", "teacher-attempts", func(t *testing.T, s string) string {
			return replaceOnce(t, s, `<td class="numeric">0 / 5</td>`, `<td class="numeric">1 / 5</td>`)
		}},
		{"detail pick", "teacher-attempt", func(t *testing.T, s string) string {
			return replaceOnce(t, s, `data-picked="false"`, `data-picked="true"`)
		}},
	}
	for _, m := range mutations {
		t.Run("mutation/"+m.name, func(t *testing.T) {
			orig := readGoldenPage(t, m.page)
			if slices.Equal(pageFacts(m.page, orig), pageFacts(m.page, m.mutate(t, orig))) {
				t.Errorf("%s: facts unchanged by the mutation", m.page)
			}
		})
	}

	whitespace := regexp.MustCompile(`>\s+<`)
	tolerances := []struct {
		name   string
		pages  []string
		change func(t *testing.T, s string) string
	}{
		{"class on main", goldenPages, func(t *testing.T, s string) string {
			return replaceOnce(t, s, `<main `, `<main class="x" `)
		}},
		{"white space between tags", goldenPages, func(t *testing.T, s string) string {
			return whitespace.ReplaceAllString(s, ">\n\t <")
		}},
		{"results-live wrapper", []string{"teacher-attempts"}, func(t *testing.T, s string) string {
			s = replaceOnce(t, s, `<div class="results-bar">`, `<div id="results-live"><div class="results-bar">`)
			return replaceOnce(t, s, "</table>\n  </div>", "</table>\n  </div></div>")
		}},
		{"answers heading", []string{"teacher-attempt"}, func(t *testing.T, s string) string {
			return replaceOnce(t, s, `<section class="detail-question"`, `<h2>Answers</h2><section class="detail-question"`)
		}},
	}
	for _, c := range tolerances {
		for _, page := range c.pages {
			t.Run("tolerance/"+c.name+"/"+page, func(t *testing.T) {
				orig := readGoldenPage(t, page)
				changed := c.change(t, orig)
				if changed == orig {
					t.Fatal("change left the page as it was")
				}
				want, got := pageFacts(page, orig), pageFacts(page, changed)
				if !slices.Equal(want, got) {
					t.Errorf("facts changed:\nwant:\n%s\ngot:\n%s", strings.Join(want, "\n"), strings.Join(got, "\n"))
				}
			})
		}
	}
}
