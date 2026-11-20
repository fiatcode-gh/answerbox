package web

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fiatcode-gh/answerbox/internal/exam"
	"github.com/fiatcode-gh/answerbox/internal/shortcode"
)

// requireInOrder fails unless each of parts occurs in body after the one
// before it.
func requireInOrder(t *testing.T, label, body string, parts ...string) {
	t.Helper()
	at := 0
	for _, p := range parts {
		i := strings.Index(body[at:], p)
		if i < 0 {
			t.Errorf("%s: %q missing after byte %d (want the order %q)", label, p, at, parts)
			return
		}
		at += i + len(p)
	}
}

// requireCount fails unless part occurs exactly n times in body.
func requireCount(t *testing.T, label, body, part string, n int) {
	t.Helper()
	if got := strings.Count(body, part); got != n {
		t.Errorf("%s: %q occurs %d times, want %d", label, part, got, n)
	}
}

func TestLandingV3_PlainPage(t *testing.T) {
	env := newTestEnv(t)
	body := do(env.h, "GET", "/", nil).Body.String()

	for _, want := range []string{
		`class="landing-page landing-split"`,
		`<main class="landing on-board" id="landing">`,
		"Run the exam.",
		"Skip the marking.",
		"Less paper, less marking,",
		"Marking is done for you",
		"Works on the phones they already have",
		"See who is still working",
		"Your data stays on your server",
		"From the first question to the grade book",
		"Write or import",
		"Schedules",
		"Projector view",
		"Optional monitor",
		"CSV export",
		"Revision history",
		"Small enough to run yourself",
		"ghcr.io/fiatcode-gh/answerbox:latest",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET / lacks %q", want)
		}
	}
	requireCount(t, "GET /", body, "<h1", 1)
	requireInOrder(t, "GET /", body, `</main>`, `section class="about"`, `id="why"`, `class="features"`, `class="tech"`, `class="site-foot"`)
	requireCount(t, "GET /", body, `href="#why"`, 1)
	requireCount(t, "GET /", body, `href="/admin/login"`, 2)
	if m := regexp.MustCompile(`(?i)\b(?:free|open source|licence|license)\b`).FindString(body); m != "" {
		t.Errorf("GET / claims %q", m)
	}
}

func TestLanding_NoPicture(t *testing.T) {
	env := newTestEnv(t)
	body := do(env.h, "GET", "/", nil).Body.String()

	for _, unwanted := range []string{"<img", "<picture", ".webp", "features-grid"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("GET / holds %q", unwanted)
		}
	}
	requireCount(t, "GET /", body, `class="feature-group"`, 3)
	if rec := do(env.h, "GET", "/static/landing/preview-exam-light.webp", nil); rec.Code != http.StatusNotFound {
		t.Errorf("GET the old preview = %d, want 404", rec.Code)
	}
}

// attrValue returns the value of the attribute name in tag, and whether the
// tag has it.
func attrValue(tag, name string) (string, bool) {
	m := regexp.MustCompile(`\s` + regexp.QuoteMeta(name) + `(?:="([^"]*)")?[\s>]`).FindStringSubmatch(tag)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// entryModesTag returns the opening tag of the tabbed entry card.
func entryModesTag(body string) string {
	return regexp.MustCompile(`<div class="entry-modes"[^>]*>`).FindString(body)
}

// requireTabs checks the tab markup without scripts: two radios, the tab row
// and both panels, Join open unless resumeOpen.
func requireTabs(t *testing.T, label, body string, resumeOpen bool) {
	t.Helper()
	join, resume := inputTag(body, "tab-join-r"), inputTag(body, "tab-resume-r")
	for _, tag := range []string{join, resume} {
		if !strings.Contains(tag, `type="radio"`) || !strings.Contains(tag, `name="entry-tab"`) {
			t.Errorf("%s: tab radio %q", label, tag)
		}
	}
	if _, on := attrValue(join, "checked"); on == resumeOpen {
		t.Errorf("%s: Join radio %q, want checked %v", label, join, !resumeOpen)
	}
	if _, on := attrValue(resume, "checked"); on != resumeOpen {
		t.Errorf("%s: Resume radio %q, want checked %v", label, resume, resumeOpen)
	}
	if !regexp.MustCompile(`<label for="tab-join-r" class="tab tab-join"[^>]*>Join</label>`).MatchString(body) ||
		!regexp.MustCompile(`<label for="tab-resume-r" class="tab tab-resume"[^>]*>Resume</label>`).MatchString(body) {
		t.Errorf("%s: the tab labels are not Join and Resume", label)
	}
	for _, unwanted := range []string{"<details", `role="tab"`, "Join exam", "Continue an interrupted exam"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("%s: holds %q", label, unwanted)
		}
	}
}

func TestEntry_Tabs(t *testing.T) {
	env := newTestEnv(t)
	body := do(env.h, "GET", "/", nil).Body.String()
	requireTabs(t, "GET /", body, false)
	requireInOrder(t, "GET /", body, `id="tab-join-r"`, `id="tab-resume-r"`, `id="lead-join"`, `id="lead-resume"`,
		`class="tablist"`, `<div class="entry">`, `id="panel-join"`, `<form id="start-form"`, `id="panel-resume"`, `<form id="resume-form"`)

	code := env.examCode(t, env.openExam(t, simpleExam("score")))
	page := do(env.h, "GET", "/?code="+code, nil).Body.String()
	requireTabs(t, "code page", page, false)
	requireInOrder(t, "code page", page, `id="tab-join-r"`, `id="tab-resume-r"`,
		`class="tablist"`, `<div class="entry">`, `id="panel-join"`, `<form id="start-form"`, `id="panel-resume"`, `<form id="resume-form"`)
	if strings.Contains(page, `id="lead-`) {
		t.Error("code page holds a lead line")
	}
}

func TestEntry_CodeBoxes(t *testing.T) {
	env := newTestEnv(t)
	body := do(env.h, "GET", "/", nil).Body.String()

	check := func(id string, want map[string]string, unwanted ...string) {
		t.Helper()
		tag := inputTag(body, id)
		for name, value := range want {
			if got, ok := attrValue(tag, name); !ok || got != value {
				t.Errorf("%s: %s = %q (present %v), want %q: %s", id, name, got, ok, value, tag)
			}
		}
		for _, name := range unwanted {
			if _, ok := attrValue(tag, name); ok {
				t.Errorf("%s has %s: %s", id, name, tag)
			}
		}
	}
	check("start-code", map[string]string{
		"class": "mask-input", "maxlength": "16", "placeholder": "______", "data-symbols": "6", "data-link": "",
		"autocapitalize": "characters", "aria-describedby": "code-hint code-msg",
	})
	check("code", map[string]string{
		"class": "mask-input", "maxlength": "16", "placeholder": "____-____", "data-symbols": "8", "data-group": "4",
		"value": "", "aria-describedby": "resume-hint resume-msg",
	}, "data-link")

	modes := entryModesTag(body)
	if got, _ := attrValue(modes, "data-alphabet"); got != shortcode.Alphabet {
		t.Errorf("data-alphabet = %q, want %q", got, shortcode.Alphabet)
	}
	missing := func(from string) string {
		var b strings.Builder
		for _, r := range from {
			if !strings.ContainsRune(shortcode.Alphabet, r) {
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	words := func(attr, class string) string {
		text, _ := attrValue(modes, attr)
		return strings.Join(regexp.MustCompile(`\b[`+class+`]\b`).FindAllString(text, -1), "")
	}
	letters := []rune(words("data-refuse-letters", "A-Z"))
	slices.Sort(letters)
	if got, want := string(letters), missing("ABCDEFGHIJKLMNOPQRSTUVWXYZ"); got != want {
		t.Errorf("data-refuse-letters names %q, want %q", got, want)
	}
	if got, want := words("data-refuse-digits", "0-9"), missing("0123456789"); got != want {
		t.Errorf("data-refuse-digits names %q, want %q", got, want)
	}
	if text, _ := attrValue(modes, "data-refuse-other"); text == "" {
		t.Errorf("entry card lacks data-refuse-other: %s", modes)
	}
}

// autofocusIDs returns the id of each tag in body that has autofocus.
func autofocusIDs(body string) []string {
	var ids []string
	for _, tag := range regexp.MustCompile(`<[a-z]+\s[^>]*\sautofocus[^>]*>`).FindAllString(body, -1) {
		id, _ := attrValue(tag, "id")
		ids = append(ids, id)
	}
	return ids
}

func TestEntry_OneAutofocus(t *testing.T) {
	env := newTestEnv(t)
	code := env.examCode(t, env.openExam(t, simpleExam("score")))
	jakarta := newJakartaEnv(t)
	early := jakarta.examCode(t, jakarta.scheduledExam(t, geometryExam, exam.Schedule{OpensAt: time.Date(2026, 11, 14, 17, 0, 0, 0, jakarta.deps.Location)}))

	for _, c := range []struct {
		label, body, want string
	}{
		{"GET /", do(env.h, "GET", "/", nil).Body.String(), "start-code"},
		{"code page", do(env.h, "GET", "/?code="+code, nil).Body.String(), "name"},
		{"refused start", postForm(env.h, "/start", url.Values{"code": {otherCode(code)}, "name": {"Ada"}}, nil).Body.String(), "start-code"},
		{"refused resume", postForm(env.h, "/resume", url.Values{"code": {"ZZZZ-ZZZZ"}}, nil).Body.String(), "code"},
		{"early start", postForm(jakarta.h, "/start", url.Values{"code": {early}, "name": {"Ada"}}, nil).Body.String(), "name"},
	} {
		requireCount(t, c.label, c.body, " autofocus", 1)
		if got := autofocusIDs(c.body); len(got) != 1 || got[0] != c.want {
			t.Errorf("%s: autofocus on %q, want %q", c.label, got, c.want)
		}
	}
}

func TestEntry_RefusedResume(t *testing.T) {
	env := newTestEnv(t)
	rec := postForm(env.h, "/resume", url.Values{"code": {" zz zz-zzzz "}}, nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("POST /resume = %d, want 422", rec.Code)
	}
	body := rec.Body.String()
	requireTabs(t, "refused resume", body, true)
	requireCount(t, "refused resume", body, `role="alert"`, 1)
	requireInOrder(t, "refused resume", body, `id="panel-resume"`, `role="alert" class="alert" id="resume-alert"`, `<form id="resume-form"`)
	_, join, _ := strings.Cut(body, `id="panel-join"`)
	if join, _, _ = strings.Cut(join, `id="panel-resume"`); strings.Contains(join, `role="alert"`) {
		t.Error("refused resume: the Join panel holds an alert")
	}
	tag := inputTag(body, "code")
	for _, want := range []string{`value=" zz zz-zzzz "`, `aria-invalid="true"`, " autofocus", `aria-describedby="resume-alert resume-hint resume-msg"`} {
		if !strings.Contains(tag, want) {
			t.Errorf("refused resume: code box lacks %q: %s", want, tag)
		}
	}

	if body := postForm(env.h, "/resume", url.Values{"code": {`"><b>x`}}, nil).Body.String(); strings.Contains(body, "<b>x") {
		t.Errorf("refused resume echoes markup:\n%s", inputTag(body, "code"))
	}
}

func TestEntry_StartAlertTied(t *testing.T) {
	env := newTestEnv(t)
	code := env.examCode(t, env.openExam(t, simpleExam("score")))

	body := postForm(env.h, "/start", url.Values{"code": {otherCode(code)}, "name": {"Ada"}}, nil).Body.String()
	if got, _ := attrValue(inputTag(body, "start-code"), "aria-describedby"); got != "start-alert code-hint code-msg" {
		t.Errorf("unknown code: start-code aria-describedby = %q", got)
	}
	if !strings.Contains(body, `role="alert" class="alert" id="start-alert"`) {
		t.Error("unknown code: the alert has no id start-alert")
	}

	body = postForm(env.h, "/start", url.Values{"code": {code}, "name": {" "}}, nil).Body.String()
	if got, _ := attrValue(inputTag(body, "name"), "aria-describedby"); got != "start-alert" {
		t.Errorf("bad name: name aria-describedby = %q", got)
	}
}

func TestLandingV3_OnlyEntryWithACode(t *testing.T) {
	env := newTestEnv(t)
	code := env.examCode(t, env.openExam(t, simpleExam("score")))
	body := do(env.h, "GET", "/?code="+code, nil).Body.String()

	for _, unwanted := range []string{"landing-split", "on-board", `class="about"`, `id="why"`, `class="site-foot"`, "landing-lead"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("code page holds %q", unwanted)
		}
	}
	if !strings.Contains(body, `<a class="brandline" href="/">`) {
		t.Error("code page lacks the brand link to /")
	}
	requireCount(t, "code page", body, "<h1", 1)
	requireCount(t, "code page", body, "·", 1)
}

// requireRefusedLayout checks what every refused page shares: the centred
// column without the product sections.
func requireRefusedLayout(t *testing.T, label, body string) {
	t.Helper()
	for _, unwanted := range []string{"landing-split", `id="why"`} {
		if strings.Contains(body, unwanted) {
			t.Errorf("%s: holds %q", label, unwanted)
		}
	}
}

// requireAlertOpensCard checks that the one alert sits in the entry card,
// before the start form.
func requireAlertOpensCard(t *testing.T, label, body string) {
	t.Helper()
	requireCount(t, label, body, `role="alert"`, 1)
	requireInOrder(t, label, body, `<div class="entry">`, `role="alert"`, `<form id="start-form"`)
}

func invalid(tag string) bool { return strings.Contains(tag, `aria-invalid="true"`) }

func TestLandingV3_Refusals(t *testing.T) {
	env := newTestEnv(t)
	code := env.examCode(t, env.openExam(t, simpleExam("score")))
	unknown := otherCode(code)

	t.Run("unknown code link", func(t *testing.T) {
		body := do(env.h, "GET", "/?code="+unknown, nil).Body.String()
		requireRefusedLayout(t, "GET /?code=unknown", body)
		requireAlertOpensCard(t, "GET /?code=unknown", body)
		if !invalid(inputTag(body, "start-code")) || invalid(inputTag(body, "name")) {
			t.Errorf("want only the code box invalid: %q %q", inputTag(body, "start-code"), inputTag(body, "name"))
		}
	})

	t.Run("bad name", func(t *testing.T) {
		rec := postForm(env.h, "/start", url.Values{"code": {code}, "name": {" "}}, nil)
		body := rec.Body.String()
		requireRefusedLayout(t, "bad name", body)
		requireAlertOpensCard(t, "bad name", body)
		start := inputTag(body, "start-code")
		if !invalid(inputTag(body, "name")) || invalid(start) {
			t.Errorf("want only the name box invalid: %q %q", start, inputTag(body, "name"))
		}
		if !strings.Contains(start, `value="`+code+`"`) {
			t.Errorf("bad name: code box %q lost the typed code", start)
		}
	})

	t.Run("unknown code start", func(t *testing.T) {
		body := postForm(env.h, "/start", url.Values{"code": {unknown}, "name": {"Ada"}}, nil).Body.String()
		requireRefusedLayout(t, "unknown start", body)
		requireAlertOpensCard(t, "unknown start", body)
		start := inputTag(body, "start-code")
		if !invalid(start) || !strings.Contains(start, `value="`+unknown+`"`) || invalid(inputTag(body, "name")) {
			t.Errorf("want the code box invalid with the typed code: %q %q", start, inputTag(body, "name"))
		}
	})

	t.Run("refused resume code", func(t *testing.T) {
		body := postForm(env.h, "/resume", url.Values{"code": {"ZZZZ-ZZZZ"}}, nil).Body.String()
		requireRefusedLayout(t, "refused resume", body)
		requireCount(t, "refused resume", body, `role="alert"`, 1)
		requireInOrder(t, "refused resume", body, `id="panel-resume"`, `role="alert"`, `<form id="resume-form"`)
		_, join, _ := strings.Cut(body, `id="panel-join"`)
		if join, _, _ = strings.Cut(join, `id="panel-resume"`); strings.Contains(join, `role="alert"`) {
			t.Error("refused resume: the Join panel holds an alert")
		}
		if !invalid(inputTag(body, "code")) || invalid(inputTag(body, "start-code")) || invalid(inputTag(body, "name")) {
			t.Errorf("want only the resume box invalid: %q", inputTag(body, "code"))
		}
	})

	t.Run("early start", func(t *testing.T) {
		env := newJakartaEnv(t)
		id := env.scheduledExam(t, geometryExam, exam.Schedule{OpensAt: time.Date(2026, 11, 14, 17, 0, 0, 0, env.deps.Location)})
		body := postForm(env.h, "/start", url.Values{"code": {env.examCode(t, id)}, "name": {"Ada"}}, nil).Body.String()
		requireRefusedLayout(t, "early start", body)
		if strings.Contains(body, "aria-invalid") {
			t.Error("early start marks a field invalid")
		}
	})
}

func TestLandingV3_UnknownAndClosedStayEqual(t *testing.T) {
	env := newTestEnv(t)
	openCode := env.examCode(t, env.openExam(t, simpleExam("score")))
	closedID := env.openExam(t, strings.Replace(simpleExam("score"), "# Algebra & Co", "# Geometry", 1))
	if err := env.exams.Close(context.Background(), env.owner, closedID); err != nil {
		t.Fatal(err)
	}
	unknown := do(env.h, "GET", "/?code="+otherCode(openCode), nil).Body.String()
	closed := do(env.h, "GET", "/?code="+env.examCode(t, closedID), nil).Body.String()
	if unknown != closed {
		t.Errorf("unknown and closed codes differ:\n%s\n---\n%s", unknown, closed)
	}
	if !invalid(inputTag(closed, "start-code")) {
		t.Errorf("closed code: code box %q is not marked invalid", inputTag(closed, "start-code"))
	}
}

var entryScript = regexp.MustCompile(`<script src="(/static/entry\.js\?v=[0-9a-f]{8})" defer></script>`)

func TestEntry_Script(t *testing.T) {
	env := newTestEnv(t)
	code := env.examCode(t, env.openExam(t, simpleExam("score")))
	for label, body := range map[string]string{
		"GET /":          do(env.h, "GET", "/", nil).Body.String(),
		"code page":      do(env.h, "GET", "/?code="+code, nil).Body.String(),
		"refused start":  postForm(env.h, "/start", url.Values{"code": {otherCode(code)}, "name": {"Ada"}}, nil).Body.String(),
		"refused resume": postForm(env.h, "/resume", url.Values{"code": {"ZZZZ-ZZZZ"}}, nil).Body.String(),
	} {
		requireCount(t, label, body, "<script", 1)
		m := entryScript.FindStringSubmatch(body)
		if m == nil {
			t.Errorf("%s does not load entry.js", label)
			continue
		}
		rec := do(env.h, "GET", m[1], nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "javascript") ||
			rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Errorf("GET %s = %d %q %q, want 200 JavaScript immutable", m[1], rec.Code,
				rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"))
		}
	}
	for _, path := range []string{"/admin/login", "/no/such/page"} {
		if body := do(env.h, "GET", path, nil).Body.String(); strings.Contains(body, "<script") {
			t.Errorf("GET %s holds a <script", path)
		}
	}
}
