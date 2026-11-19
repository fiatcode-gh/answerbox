package web

import (
	"bytes"
	"context"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fiatcode-gh/answerbox/internal/attempt"
)

var updateGolden = flag.Bool("update-golden", false, "write the page goldens of TestUnsectionedPages_Unchanged after their facts compare equal")

// TestUnsectionedPages_Unchanged pins the pages of an exam without sections
// to the output of release 293ab19. The goldens were written on that release.
//
// During the UI revamp the six page goldens are compared by pageFacts against
// the 293ab19 files, so the markup around the facts may change while question
// and option order, picks, correctness, scores and sections may not;
// attempts.csv is compared byte for byte. -update-golden checks the facts,
// refuses on any difference and then writes the six page goldens, never the
// CSV golden. Task 14 of the revamp plan re-baselines the pages once and
// restores byte comparison for all seven files: from then on a difference is
// a defect.
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
		if g.name == "attempts.csv" {
			if !bytes.Equal(want, []byte(g.body)) {
				t.Fatalf("golden %s differs; got:\n%s", g.name, g.body)
			}
			continue
		}
		wantFacts, gotFacts := pageFacts(g.name, string(want)), pageFacts(g.name, g.body)
		if !slices.Equal(wantFacts, gotFacts) {
			i := 0
			for i < len(wantFacts) && i < len(gotFacts) && wantFacts[i] == gotFacts[i] {
				i++
			}
			line := func(facts []string) string {
				if i < len(facts) {
					return facts[i]
				}
				return "(no more facts)"
			}
			t.Fatalf("golden %s: facts differ at fact %d\nwant: %s\ngot:  %s\npage:\n%s", g.name, i+1, line(wantFacts), line(gotFacts), g.body)
		}
	}
	if !*updateGolden {
		return
	}
	for _, g := range got {
		if g.name == "attempts.csv" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, g.name+".golden"), []byte(g.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
