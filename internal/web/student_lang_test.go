package web

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fiatcode-gh/answerbox/internal/shortcode"
)

// inLang is header with the language cookie of l added.
func inLang(l lang, header http.Header) http.Header {
	h := header.Clone()
	if h == nil {
		h = http.Header{}
	}
	h.Add("Cookie", langCookieName+"="+string(l))
	return h
}

// attr is the decoded value of the attribute name in tag.
func attr(tag, name string) string {
	v, _ := attrValue(tag, name)
	return html.UnescapeString(v)
}

// tagWithID is the opening tag of the element with the given id.
func tagWithID(body, id string) string {
	return regexp.MustCompile(`<[a-z]+\s[^>]*\bid="` + regexp.QuoteMeta(id) + `"[^>]*>`).FindString(body)
}

// textOf is the decoded text of the element with the given id, which holds
// no other element.
func textOf(body, id string) string {
	m := regexp.MustCompile(`\bid="` + regexp.QuoteMeta(id) + `"[^>]*>([^<]*)<`).FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return html.UnescapeString(m[1])
}

func TestLanding_Indonesian(t *testing.T) {
	env := newTestEnv(t)
	code := env.examCode(t, env.openExam(t, simpleExam("score")))
	pages := []struct {
		label string
		get   func(l lang) (int, string)
	}{
		{"GET /", func(l lang) (int, string) {
			rec := do(env.h, "GET", "/", inLang(l, nil))
			return rec.Code, rec.Body.String()
		}},
		{"code page", func(l lang) (int, string) {
			rec := do(env.h, "GET", "/?code="+code, inLang(l, nil))
			return rec.Code, rec.Body.String()
		}},
		{"refused start", func(l lang) (int, string) {
			rec := postForm(env.h, "/start", url.Values{"code": {otherCode(code)}, "name": {"Ada"}}, inLang(l, nil))
			return rec.Code, rec.Body.String()
		}},
	}
	for _, p := range pages {
		enStatus, en := p.get(langEN)
		idStatus, id := p.get(langID)
		if enStatus != idStatus {
			t.Errorf("%s: status en %d, id %d", p.label, enStatus, idStatus)
		}
		for _, want := range []string{
			`<label for="name">` + idText(t, "landing.your_name") + `</label>`,
			`<button type="submit">` + idText(t, "landing.start_exam") + `</button>`,
			`<label for="code">` + idText(t, "landing.resume_code") + `</label>`,
			`<button type="submit">` + idText(t, "landing.continue_exam") + `</button>`,
			idText(t, "landing.timer_note"),
		} {
			if !strings.Contains(id, want) {
				t.Errorf("%s: Indonesian page lacks %q", p.label, want)
			}
		}
		if p.label == "code page" {
			if !strings.Contains(id, `<p class="landing-code">`+idText(t, "landing.exam_code")+` <strong class="code">`) ||
				!strings.Contains(id, `<a href="/">`+idText(t, "landing.use_another_code")+`</a>`) {
				t.Errorf("%s: the exam code line is not Indonesian:\n%s", p.label, id)
			}
		} else {
			for _, want := range []string{
				`<p class="landing-lead" id="lead-join">` + idText(t, "landing.lead_join") + `</p>`,
				`<p class="landing-lead" id="lead-resume">` + idText(t, "landing.lead_resume") + `</p>`,
				`<label for="start-code">` + idText(t, "landing.exam_code") + `</label>`,
				`<p class="landing-hint" id="code-hint">` + idText(t, "landing.code_hint") + `</p>`,
			} {
				if !strings.Contains(id, want) {
					t.Errorf("%s: Indonesian page lacks %q", p.label, want)
				}
			}
			if got := attr(inputTag(id, "start-code"), "data-refuse-link"); got != idText(t, "landing.refuse_link") {
				t.Errorf("%s: start-code data-refuse-link = %q", p.label, got)
			}
		}
		if p.label == "refused start" {
			if got := alertText(id); got != idText(t, "landing.alert.no_open_exam") {
				t.Errorf("%s: alert = %q, want the Indonesian refusal", p.label, got)
			}
		}

		modes := entryModesTag(id)
		for attrName, key := range map[string]string{
			"data-refuse-letters": "landing.refuse_letters",
			"data-refuse-digits":  "landing.refuse_digits",
			"data-refuse-other":   "landing.refuse_other",
		} {
			if got := attr(modes, attrName); got != idText(t, key) {
				t.Errorf("%s: %s = %q, want %q", p.label, attrName, got, idText(t, key))
			}
		}
		if got := attr(modes, "data-alphabet"); got != shortcode.Alphabet {
			t.Errorf("%s: data-alphabet = %q", p.label, got)
		}
		tablist := regexp.MustCompile(`<div class="tablist"[^>]*>`).FindString(id)
		if got := attr(tablist, "data-label"); got != idText(t, "landing.tabs_label") {
			t.Errorf("%s: tablist data-label = %q", p.label, got)
		}
		if got := attr(inputTag(id, "code"), "data-refuse-link"); got != idText(t, "landing.resume_refuse_link") {
			t.Errorf("%s: resume data-refuse-link = %q", p.label, got)
		}

		for _, field := range []string{"start-code", "name", "code"} {
			for _, a := range []string{"aria-describedby", "aria-invalid"} {
				enV, enOK := attrValue(inputTag(en, field), a)
				idV, idOK := attrValue(inputTag(id, field), a)
				if enV != idV || enOK != idOK {
					t.Errorf("%s: %s %s en %q (%v), id %q (%v)", p.label, field, a, enV, enOK, idV, idOK)
				}
			}
		}
		if enIDs, idIDs := autofocusIDs(en), autofocusIDs(id); !slices.Equal(enIDs, idIDs) || len(idIDs) != 1 {
			t.Errorf("%s: autofocus en %q, id %q", p.label, enIDs, idIDs)
		}
	}
}

// examTextAttrs are the attributes #exam-text holds for exam.js.
var examTextAttrs = []string{
	"data-saved", "data-saving", "data-not-saved", "data-all-saved", "data-retrying", "data-check-answer",
	"data-choose-again", "data-waiting", "data-submit", "data-answered", "data-summary", "data-missing-one",
	"data-missing-other", "data-summary-end", "data-announce-600", "data-announce-300", "data-announce-60",
}

func TestExamPage_ScriptWords(t *testing.T) {
	env := newTestEnv(t)
	env.openExam(t, examText("none", "score", 3, 2))
	cookie := env.start(t, "Ada")
	qs := pageQuestions(t, env.examPage(t, cookie))
	requireJSON(t, env.answer(cookie, qs[0].ID, qs[0].Options[0]), http.StatusOK, `{"saved":true,"remaining_ms":3600000}`)

	for _, l := range []lang{langEN, langID} {
		body := env.examPage(t, inLang(l, cookie))
		tag := tagWithID(body, "exam-text")
		if tag == "" {
			t.Fatalf("%s: no #exam-text:\n%s", l, body)
		}
		if strings.Index(body, tag) < strings.Index(body, "</main>") {
			t.Errorf("%s: #exam-text is inside <main>", l)
		}
		if _, hidden := attrValue(tag, "hidden"); !hidden {
			t.Errorf("%s: #exam-text is not hidden: %s", l, tag)
		}
		text := map[string]string{}
		for _, a := range examTextAttrs {
			text[a] = attr(tag, a)
			if text[a] == "" {
				t.Errorf("%s: #exam-text lacks %s: %s", l, a, tag)
			}
		}
		if got := textOf(body, "save-state"); got != text["data-all-saved"] {
			t.Errorf("%s: #save-state = %q, data-all-saved = %q", l, got, text["data-all-saved"])
		}
		if got := textOf(body, "submit-confirm"); got != text["data-submit"] {
			t.Errorf("%s: #submit-confirm = %q, data-submit = %q", l, got, text["data-submit"])
		}
		filled := strings.NewReplacer("{done}", "1", "{total}", "3").Replace(text["data-answered"])
		if got := textOf(body, "answered-count"); got != filled {
			t.Errorf("%s: #answered-count = %q, want data-answered filled: %q", l, got, filled)
		}
		if !strings.Contains(body, `<p class="answered-summary">`+filled+`</p>`) {
			t.Errorf("%s: .answered-summary is not %q", l, filled)
		}
		if !strings.Contains(body, `<span class="q-state" data-state="saved">`+text["data-saved"]+`</span>`) {
			t.Errorf("%s: the answered question's state is not %q", l, text["data-saved"])
		}
		for a, holes := range map[string][]string{
			"data-answered":      {"{done}", "{total}"},
			"data-summary":       {"{done}", "{total}"},
			"data-missing-one":   {"{n}"},
			"data-missing-other": {"{n}"},
		} {
			for _, h := range holes {
				if !strings.Contains(text[a], h) {
					t.Errorf("%s: %s = %q lacks %s", l, a, text[a], h)
				}
			}
		}
		if l == langID && text["data-saved"] != idText(t, "exam.saved") {
			t.Errorf("id: data-saved = %q, want the Indonesian", text["data-saved"])
		}
	}
}

func TestResultPage_Indonesian(t *testing.T) {
	env := newTestEnv(t)
	id := env.openExam(t, "# Ujian\nshuffle: none\nshow_result: review\n\n1. Satu\n- a *\n- b\n\n2. Dua\n- c\n- d *\n\n3. Tiga\n= x\n\n4. Empat\n- e *\n- f\n")
	qs, err := env.exams.Questions(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := env.startAttempt(t, id, "Ada")
	env.pick(t, token, qs[0], 1)
	env.pick(t, token, qs[1], 1)
	if _, err := env.attempts.SaveText(context.Background(), token, qs[2].ID, "x"); err != nil {
		t.Fatal(err)
	}
	env.submitAttempt(t, token)

	en := env.examPage(t, inLang(langEN, studentCookie(token)))
	idBody := env.examPage(t, inLang(langID, studentCookie(token)))
	for _, want := range []string{
		"<h1>" + idText(t, "result.submitted") + "</h1>",
		`<p class="score-caption">` + idText(t, "result.correct_answers") + `</p>`,
		`<h2 class="review-heading">` + idText(t, "result.your_answers") + `</h2>`,
		`<p class="eyebrow">` + fmt.Sprintf(idText(t, "result.question_of"), 1, 4) + `</p>`,
		`<p class="no-answer">` + idText(t, "result.no_answer") + `</p>`,
		`<p class="review-typed">` + idText(t, "result.your_answer_typed") + ` <span class="typed-text">x</span> <span class="review-mark">` + idText(t, "result.right") + `</span></p>`,
		`<p class="accepted-label">` + idText(t, "result.accepted_answers") + `</p>`,
		`<span class="review-mark">` + idText(t, "result.picked_correct") + `</span>`,
		`<span class="review-mark">` + idText(t, "result.correct_answer") + `</span>`,
		`<span class="review-mark">` + idText(t, "result.picked") + `</span>`,
		`<a class="button secondary" href="/">` + idText(t, "result.back") + `</a>`,
	} {
		if !strings.Contains(idBody, want) {
			t.Errorf("Indonesian result lacks %q", want)
		}
	}
	for _, key := range []string{"result.right", "result.picked_correct", "result.correct_answer"} {
		if !strings.HasPrefix(idText(t, key), "✓ ") {
			t.Errorf("%s = %q does not keep its ✓", key, idText(t, key))
		}
	}
	for _, key := range []string{"result.wrong", "result.picked"} {
		if !strings.HasPrefix(idText(t, key), "✗ ") {
			t.Errorf("%s = %q does not keep its ✗", key, idText(t, key))
		}
	}
	hooks := regexp.MustCompile(`data-(?:show|status|picked|correct|answered)="[^"]*"`)
	if got, want := hooks.FindAllString(idBody, -1), hooks.FindAllString(en, -1); !slices.Equal(got, want) || len(got) == 0 {
		t.Errorf("result hooks id %q, en %q", got, want)
	}
}

func TestResultPage_IndonesianTimedOut(t *testing.T) {
	env := newTestEnv(t)
	id := env.openExam(t, withDuration(simpleExam("none"), 5))
	token, _ := env.startAttempt(t, id, "Ada")
	env.clock = testStart.Add(6 * time.Minute)
	body := env.examPage(t, inLang(langID, studentCookie(token)))
	for _, want := range []string{
		"<h1>" + idText(t, "result.time_up") + "</h1>",
		`<p class="result-recorded">` + idText(t, "result.recorded") + `</p>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("timed-out Indonesian result lacks %q:\n%s", want, body)
		}
	}
}

func TestOpensAtAlert_Indonesian(t *testing.T) {
	env := newJakartaEnv(t)
	s, err := newServer(env.deps)
	if err != nil {
		t.Fatal(err)
	}
	jakarta := env.deps.Location
	id, en := localizer{langID}, localizer{langEN}

	env.clock = time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC) // 08:00 in Jakarta
	today := time.Date(2026, 11, 2, 17, 0, 0, 0, jakarta)
	if got, want := s.opensAtAlert(id, today), fmt.Sprintf(idText(t, "landing.alert.opens_at"), "17:00"); got != want || strings.Contains(got, "Nov") {
		t.Errorf("today, id = %q, want %q", got, want)
	}
	if got, want := s.opensAtAlert(en, today), "This exam opens at 17:00."; got != want {
		t.Errorf("today, en = %q, want %q", got, want)
	}

	env.clock = time.Date(2026, 10, 30, 1, 0, 0, 0, time.UTC)
	later := time.Date(2026, 11, 2, 8, 0, 0, 0, jakarta)
	if got, want := s.opensAtAlert(id, later), fmt.Sprintf(idText(t, "landing.alert.opens_at_on"), "08:00", "Sen, 2 Nov 2026"); got != want || !strings.Contains(got, "Sen, 2 Nov 2026") {
		t.Errorf("another day, id = %q, want %q", got, want)
	}
	if got, want := s.opensAtAlert(en, later), "This exam opens at 08:00 on Mon, 2 Nov 2026."; got != want {
		t.Errorf("another day, en = %q, want %q", got, want)
	}
}

func TestStart_RefusalsSameInBothLanguages(t *testing.T) {
	env := newTestEnv(t)
	closed := env.openExam(t, examText("none", "score", 1, 2))
	if err := env.exams.Close(context.Background(), env.owner, closed); err != nil {
		t.Fatal(err)
	}
	code := env.examCode(t, env.openExam(t, simpleExam("score")))
	cases := []struct {
		label string
		form  url.Values
		key   string
	}{
		{"no exam", url.Values{"code": {"ABCDEF"}, "name": {"Ada"}}, "landing.alert.no_open_exam"},
		{"another well-formed code", url.Values{"code": {otherCode(code)}, "name": {"Ada"}}, "landing.alert.no_open_exam"},
		{"missing code", url.Values{"name": {"Ada"}}, "landing.alert.no_open_exam"},
		{"closed exam's code", url.Values{"code": {env.examCode(t, closed)}, "name": {"Ada"}}, "landing.alert.no_open_exam"},
		{"empty name", url.Values{"code": {code}, "name": {""}}, "landing.alert.name"},
		{"blank name", url.Values{"code": {code}, "name": {"   "}}, "landing.alert.name"},
		{"long name", url.Values{"code": {code}, "name": {strings.Repeat("x", 101)}}, "landing.alert.name"},
		{"malformed code, empty name", url.Values{"code": {"abc"}, "name": {""}}, "landing.alert.name"},
	}
	for _, c := range cases {
		before := env.attemptRows(t)
		en := postForm(env.h, "/start", c.form, inLang(langEN, nil))
		id := postForm(env.h, "/start", c.form, inLang(langID, nil))
		if en.Code != http.StatusUnprocessableEntity || id.Code != en.Code {
			t.Errorf("%s: status en %d, id %d; want 422", c.label, en.Code, id.Code)
		}
		if attemptSetCookie(t, en) != nil || attemptSetCookie(t, id) != nil {
			t.Errorf("%s: a cookie was set", c.label)
		}
		if got := env.attemptRows(t); got != before {
			t.Errorf("%s: attempt rows %d → %d", c.label, before, got)
		}
		if got := alertText(id.Body.String()); got != idText(t, c.key) {
			t.Errorf("%s: id alert = %q, want %q", c.label, got, idText(t, c.key))
		}
		if got := alertText(en.Body.String()); got != enText(t, c.key) {
			t.Errorf("%s: en alert = %q, want %q", c.label, got, enText(t, c.key))
		}
		for _, field := range []string{"start-code", "name"} {
			enTag, idTag := inputTag(en.Body.String(), field), inputTag(id.Body.String(), field)
			if invalid(enTag) != invalid(idTag) || attr(enTag, "aria-describedby") != attr(idTag, "aria-describedby") ||
				attr(enTag, "value") != attr(idTag, "value") {
				t.Errorf("%s: %s en %s, id %s", c.label, field, enTag, idTag)
			}
		}
	}
}
