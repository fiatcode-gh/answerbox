package web

import (
	"context"
	"encoding/csv"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/fiatcode-gh/go-exam/internal/exam"
)

// sectionedText is an exam of five questions in two sections: "Bagian A:
// Pilihan Ganda" with instructions over questions 1–2 and "Bagian B"
// without instructions over questions 3–5. Option 1 is always correct.
func sectionedText(shuffle, show string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Sectioned\nshuffle: %s\nshow_result: %s\n", shuffle, show)
	for i := 1; i <= 5; i++ {
		switch i {
		case 1:
			b.WriteString("\n## Bagian A: Pilihan Ganda\nPilih **satu** jawaban.\n")
		case 3:
			b.WriteString("\n## Bagian B\n")
		}
		fmt.Fprintf(&b, "\n%d. Question %d\n- Option %d.1 *\n- Option %d.2\n", i, i, i, i)
	}
	return b.String()
}

const (
	sectionATitle = "Bagian A: Pilihan Ganda"
	sectionABody  = "<p>Pilih <strong>satu</strong> jawaban.</p>"
	sectionBTitle = "Bagian B"
)

// sectionHead is one rendered section_head followed by the opening tag it
// stands before: the title, the instructions ("" without them) and what
// follows.
type sectionHead struct{ title, body, next string }

// sectionHeads returns every section heading of body directly followed by an
// opening tag that matches next.
func sectionHeads(body, next string) []sectionHead {
	re := regexp.MustCompile(`(?s)<div class="section-head"><h2 class="section-title">([^<]*)</h2>(?:<div class="section-body question-body">(.*?)</div>)?</div>(` + next + `)`)
	var out []sectionHead
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		out = append(out, sectionHead{m[1], strings.TrimSpace(m[2]), m[3]})
	}
	return out
}

// wantHeads checks that body has exactly two section headings, A with its
// instructions before firstA and B without instructions before firstB, both
// matched by next.
func wantHeads(t *testing.T, page, body, next, firstA, firstB string) {
	t.Helper()
	if n := strings.Count(body, `<div class="section-head">`); n != 2 {
		t.Fatalf("%s: %d section heads, want 2:\n%s", page, n, body)
	}
	heads := sectionHeads(body, next)
	want := []sectionHead{{sectionATitle, sectionABody, firstA}, {sectionBTitle, "", firstB}}
	if !reflect.DeepEqual(heads, want) {
		t.Fatalf("%s: section heads = %q, want %q", page, heads, want)
	}
}

func (env *testEnv) fileQuestions(t *testing.T, examID int64) []exam.Question {
	t.Helper()
	qs, err := env.exams.Questions(context.Background(), examID)
	if err != nil {
		t.Fatal(err)
	}
	return qs
}

func TestExamPage_SectionHeads(t *testing.T) {
	env := newTestEnv(t)
	id := env.openExam(t, sectionedText("questions", "score"))
	file := env.fileQuestions(t, id)
	token, _ := env.startAttempt(t, id, "Ayu")
	body := env.examPage(t, studentCookie(token))

	shown := pageQuestions(t, body)
	if len(shown) != 5 {
		t.Fatalf("page has %d questions, want 5", len(shown))
	}
	byNumber := map[int]string{}
	for i, q := range shown {
		byNumber[i+1] = strconv.FormatInt(q.ID, 10)
	}
	wantHeads(t, "exam page", body, `<section class="question" id="q\d+" data-question-id="\d+">`,
		fmt.Sprintf(`<section class="question" id="q1" data-question-id="%s">`, byNumber[1]),
		fmt.Sprintf(`<section class="question" id="q3" data-question-id="%s">`, byNumber[3]))

	for _, r := range [][2]int{{0, 2}, {2, 5}} {
		var got, want []int64
		for i := r[0]; i < r[1]; i++ {
			got = append(got, shown[i].ID)
			want = append(want, file[i].ID)
		}
		slices.Sort(got)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("questions %d–%d show file questions %v, want %v", r[0]+1, r[1], got, want)
		}
	}
	for n := 1; n <= 5; n++ {
		if !strings.Contains(body, fmt.Sprintf("Question %d of 5", n)) {
			t.Errorf("page lacks %q", fmt.Sprintf("Question %d of 5", n))
		}
		if !strings.Contains(body, fmt.Sprintf(`<a class="bubble" href="#q%d"`, n)) {
			t.Errorf("navigator lacks bubble #q%d", n)
		}
	}
	if n := strings.Count(body, `<a class="bubble" href="#q`); n != 5 {
		t.Errorf("navigator has %d bubbles, want 5", n)
	}
}

func TestResultPage_ReviewSectionHeads(t *testing.T) {
	env := newTestEnv(t)
	id := env.openExam(t, sectionedText("questions, options", "review"))
	file := env.fileQuestions(t, id)
	token, _ := env.startAttempt(t, id, "Ayu")
	env.pick(t, token, file[0], 1)
	env.pick(t, token, file[3], 1)
	env.submitAttempt(t, token)
	body := env.examPage(t, studentCookie(token))

	wantHeads(t, "review", body, `<section class="review-question">\s*<p class="eyebrow">Question \d+ of 5</p>`,
		"<section class=\"review-question\">\n      <p class=\"eyebrow\">Question 1 of 5</p>",
		"<section class=\"review-question\">\n      <p class=\"eyebrow\">Question 3 of 5</p>")
	if !strings.Contains(body, `<p id="score">2 / 5</p>`) {
		t.Errorf("score block does not read 2 / 5:\n%s", body)
	}
	if strings.Contains(body, "section-score") {
		t.Error("review page shows a section score")
	}

	scoreOnly := env.openExam(t, sectionedText("questions", "score"))
	token, _ = env.startAttempt(t, scoreOnly, "Budi")
	env.submitAttempt(t, token)
	if body := env.examPage(t, studentCookie(token)); strings.Contains(body, "section-head") {
		t.Errorf("score-only result shows section heads:\n%s", body)
	}
}

func TestTeacherPages_SectionHeads(t *testing.T) {
	env := newTestEnv(t)
	admin, _, _ := env.signIn(t)
	id := env.openExam(t, sectionedText("questions", "score"))
	_, aid := env.startAttempt(t, id, "Ayu")

	for page, path := range map[string]string{"exam page": examPath(id), "revision page": examPath(id) + "/revisions/1"} {
		wantHeads(t, page, getOK(t, env, path, admin), `<section class="preview-question" id="q\d+">`,
			`<section class="preview-question" id="q1">`, `<section class="preview-question" id="q3">`)
	}
	wantHeads(t, "attempt page", getOK(t, env, fmt.Sprintf("%s/%d", attemptsPath(id), aid), admin),
		`<section class="detail-question" id="q\d+">`,
		`<section class="detail-question" id="q1">`, `<section class="detail-question" id="q3">`)
}

// sectionAttempts gives a sectioned exam two attempts: Ayu answers 1, 3 and 4
// right and 2 wrong (3 / 5: 1 / 2 and 2 / 3), Budi answers 5 right (1 / 5:
// 0 / 2 and 1 / 3).
func (env *testEnv) sectionAttempts(t *testing.T, examID int64) (ayu, budi int64) {
	t.Helper()
	qs := env.fileQuestions(t, examID)
	ayuToken, ayu := env.startAttempt(t, examID, "Ayu")
	env.pick(t, ayuToken, qs[0], 1)
	env.pick(t, ayuToken, qs[1], 2)
	env.pick(t, ayuToken, qs[2], 1)
	env.pick(t, ayuToken, qs[3], 1)
	budiToken, budi := env.startAttempt(t, examID, "Budi")
	env.pick(t, budiToken, qs[4], 1)
	return ayu, budi
}

var headerCellRe = regexp.MustCompile(`(?s)<th[^>]*>(.*?)</th>`)

func TestAttempts_SectionColumns(t *testing.T) {
	env := newTestEnv(t)
	admin, _, _ := env.signIn(t)
	id := env.openExam(t, sectionedText("none", "score"))
	ayu, budi := env.sectionAttempts(t, id)

	body := getOK(t, env, attemptsPath(id), admin)
	var headers []string
	for _, m := range headerCellRe.FindAllStringSubmatch(body, -1) {
		headers = append(headers, strings.Join(strings.Fields(tagRe.ReplaceAllString(m[1], "")), " "))
	}
	i := slices.Index(headers, "Score")
	if i < 0 || i+3 >= len(headers) || !reflect.DeepEqual(headers[i:i+4], []string{"Score", sectionATitle, sectionBTitle, "Time left"}) {
		t.Fatalf("header cells = %q, want Score, the two section titles, Time left", headers)
	}
	for _, title := range []string{sectionATitle, sectionBTitle} {
		if !strings.Contains(body, `<th scope="col" class="wrap section-score">`+title+`</th>`) {
			t.Errorf("no th.section-score for %q", title)
		}
	}

	scores := func(body string) [][]string {
		var out [][]string
		for _, row := range tableRows(body) {
			out = append(out, row[:9]) // ID, name, started, finished, status, score, two sections, time left
		}
		return out
	}
	ayuRow := []string{fmt.Sprint(ayu), "Ayu", "", "", "", "3 / 5", "1 / 2", "2 / 3", ""}
	budiRow := []string{fmt.Sprint(budi), "Budi", "", "", "", "1 / 5", "0 / 2", "1 / 3", ""}
	for _, tc := range []struct {
		query string
		want  [][]string
	}{
		{"", [][]string{ayuRow, budiRow}},
		{"?sort=score&dir=asc", [][]string{budiRow, ayuRow}},
		{"?sort=score&dir=desc", [][]string{ayuRow, budiRow}},
	} {
		got := scores(getOK(t, env, attemptsPath(id)+tc.query, admin))
		for _, row := range got {
			row[2], row[3], row[4], row[8] = "", "", "", "" // times and status are tested elsewhere
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q: rows = %q, want %q", tc.query, got, tc.want)
		}
	}
	if n := strings.Count(body, `<td class="numeric section-score">`); n != 4 {
		t.Errorf("%d td.section-score cells, want 4", n)
	}

	plain := env.openExam(t, examText("questions", "score", 3, 2))
	env.startAttempt(t, plain, "Citra")
	if body := getOK(t, env, attemptsPath(plain), admin); strings.Contains(body, "section-score") {
		t.Errorf("exam without sections has section columns:\n%s", body)
	}
}

func TestAttempts_CSVSectionColumns(t *testing.T) {
	env := newTestEnv(t)
	admin, _, _ := env.signIn(t)
	id := env.openExam(t, sectionedText("none", "score"))
	env.sectionAttempts(t, id)

	records, err := csv.NewReader(strings.NewReader(getOK(t, env, attemptsPath(id)+".csv", admin))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"name", "status", "score", "total", sectionATitle, sectionBTitle, "started", "finished", "flags"}; !reflect.DeepEqual(records[0], want) {
		t.Fatalf("header = %q, want %q", records[0], want)
	}
	want := [][]string{{"Ayu", "in progress", "3", "5", "1", "2"}, {"Budi", "in progress", "1", "5", "0", "1"}}
	if len(records) != 3 {
		t.Fatalf("records = %q, want a header and two rows", records)
	}
	for i, w := range want {
		if got := records[i+1]; len(got) != 9 || !reflect.DeepEqual(got[:6], w) {
			t.Errorf("row %d = %q, want it to start with %q and have 9 cells", i+1, got, w)
		}
	}

	formula := env.openExam(t, strings.Replace(sectionedText("none", "score"), "## Bagian B", "## =SUM(A1)", 1))
	body := getOK(t, env, attemptsPath(formula)+".csv", admin)
	if want := "name,status,score,total,Bagian A: Pilihan Ganda,'=SUM(A1),started,finished,flags\n"; body != want {
		t.Errorf("CSV without attempts = %q, want the header alone %q", body, want)
	}
}
