package web

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fiatcode-gh/answerbox/internal/attempt"
)

// TestUnsectionedPages_Unchanged pins the pages of an exam without sections.
// attempts.csv is the output of release 293ab19. The six page goldens are the
// output of the UI revamp head, written once after a check that they agree
// with the 293ab19 goldens on question order, option order, picked and correct
// state, scores and section structure. None of the seven is ever regenerated:
// a difference is a defect.
func TestUnsectionedPages_Unchanged(t *testing.T) {
	env := newTestEnv(t)
	admin, _, _ := env.signIn(t)
	id := env.openExam(t, examText("questions, options", "review", 5, 3))

	type golden struct{ name, body string }
	var got []golden
	page := func(name, body string) {
		start := strings.Index(body, "<main")
		end := strings.Index(body, "</main>")
		if start < 0 || end < start {
			t.Fatalf("%s: no <main> element:\n%s", name, body)
		}
		got = append(got, golden{name, body[start : end+len("</main>")]})
	}

	token, aid := env.startAttempt(t, id, "Ayu")
	ayu := studentCookie(token)
	body := env.examPage(t, ayu)
	page("student-exam", body)

	qs := pageQuestions(t, body)
	for _, a := range []struct{ q, o int }{{0, 0}, {2, 1}} {
		if rec := env.answer(ayu, qs[a.q].ID, qs[a.q].Options[a.o]); rec.Code != http.StatusOK {
			t.Fatalf("answer question %d = %d", a.q, rec.Code)
		}
	}
	recorded, err := env.attempts.RecordEvent(context.Background(), token, attempt.Copy, qs[1].ID)
	if err != nil || !recorded {
		t.Fatalf("RecordEvent = %v, %v; want true", recorded, err)
	}

	env.clock = testStart.Add(10 * time.Minute)
	env.submitAttempt(t, token)
	page("student-result", env.examPage(t, ayu))

	env.startAttempt(t, id, "Budi")

	page("teacher-exam", getOK(t, env, examPath(id), admin))
	page("teacher-revision", getOK(t, env, examPath(id)+"/revisions/1", admin))
	page("teacher-attempts", getOK(t, env, attemptsPath(id), admin))
	page("teacher-attempt", getOK(t, env, attemptsPath(id)+"/"+strconv.FormatInt(aid, 10), admin))
	got = append(got, golden{"attempts.csv", getOK(t, env, attemptsPath(id)+".csv", admin)})

	dir := filepath.Join("testdata", "unsectioned")
	for _, g := range got {
		path := filepath.Join(dir, g.name+".golden")
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(want, []byte(g.body)) {
			t.Fatalf("golden %s differs; got:\n%s", g.name, g.body)
		}
	}
}
