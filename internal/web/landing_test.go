package web

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/fiatcode-gh/answerbox/internal/exam"
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

func TestLandingV3_Preview(t *testing.T) {
	env := newTestEnv(t)
	body := do(env.h, "GET", "/", nil).Body.String()

	requireCount(t, "GET /", body, "<img", 1)
	img := regexp.MustCompile(`<img [^>]*>`).FindString(body)
	if want := `width="1200" height="534" loading="lazy" decoding="async"`; !strings.Contains(img, want) {
		t.Errorf("preview <img> lacks %q: %s", want, img)
	}
	alt := regexp.MustCompile(`\balt="([^"]*)"`).FindStringSubmatch(img)
	if alt == nil || strings.TrimSpace(alt[1]) == "" {
		t.Errorf("preview <img> has no alternative text: %s", img)
	} else if code := regexp.MustCompile(`\b[A-Z0-9]{6}\b`).FindString(alt[1]); code != "" {
		t.Errorf("preview alternative text names a code %q: %s", code, alt[1])
	}

	for name, re := range map[string]*regexp.Regexp{
		"light": regexp.MustCompile(`<img [^>]*\bsrc="(/static/landing/preview-exam-light\.webp\?v=[0-9a-f]{8})"`),
		"dark":  regexp.MustCompile(`<source [^>]*\bsrcset="(/static/landing/preview-exam-dark\.webp\?v=[0-9a-f]{8})"`),
	} {
		m := re.FindStringSubmatch(body)
		if m == nil {
			t.Errorf("no %s preview URL in GET /", name)
			continue
		}
		rec := do(env.h, "GET", m[1], nil)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/webp" {
			t.Errorf("GET %s = %d %q, want 200 image/webp", m[1], rec.Code, rec.Header().Get("Content-Type"))
		}
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
		requireInOrder(t, "refused resume", body, `<details class="resume" open>`, `<div class="resume-body">`, `role="alert"`, `<form id="resume-form"`)
		card, _, _ := strings.Cut(body, `<details class="resume"`)
		if _, card, _ = strings.Cut(card, `<div class="entry">`); strings.Contains(card, `role="alert"`) {
			t.Error("refused resume: the entry card holds an alert")
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

func TestPublicPages_NoScript(t *testing.T) {
	env := newTestEnv(t)
	code := env.examCode(t, env.openExam(t, simpleExam("score")))
	for _, path := range []string{"/", "/?code=" + code, "/admin/login", "/no/such/page"} {
		if body := do(env.h, "GET", path, nil).Body.String(); strings.Contains(body, "<script") {
			t.Errorf("GET %s holds a <script", path)
		}
	}
}
