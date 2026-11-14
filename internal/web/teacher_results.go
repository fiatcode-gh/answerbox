package web

import (
	"bytes"
	"cmp"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/attempt"
	"github.com/fiatcode-gh/go-exam/internal/exam"
)

// mountTeacherResults registers the results pages on the admin sub-mux.
func (s *server) mountTeacherResults(admin *http.ServeMux) {
	admin.HandleFunc("GET /admin/exams/{id}/attempts", s.attemptList)
	admin.HandleFunc("GET /admin/exams/{id}/attempts.csv", s.attemptsCSV)
	admin.HandleFunc("GET /admin/exams/{id}/attempts/{aid}", s.attemptDetail)
	admin.HandleFunc("GET /admin/exams/{id}/attempts/{aid}/delete", s.attemptDeleteConfirm)
	admin.HandleFunc("POST /admin/exams/{id}/attempts/{aid}/delete", s.attemptDelete)
}

// resultsExam resolves the {id} exam of a results route. On failure it has
// responded (404 or 500) and returns ok false.
func (s *server) resultsExam(w http.ResponseWriter, r *http.Request) (e exam.Exam, ok bool) {
	id, ok := examID(r)
	if !ok {
		s.notFound(w, r)
		return exam.Exam{}, false
	}
	e, err := s.deps.Exams.Get(r.Context(), id)
	if errors.Is(err, exam.ErrNotFound) {
		s.notFound(w, r)
		return exam.Exam{}, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return exam.Exam{}, false
	}
	return e, true
}

func attemptsPath(examID int64) string {
	return examPath(examID) + "/attempts"
}

const listTimeLayout = "2006-01-02 15:04:05"

type statusLabel struct{ Text, Badge string }

var attemptStatus = map[attempt.Status]statusLabel{
	attempt.InProgress: {"In progress", "badge-warn"},
	attempt.Submitted:  {"Submitted", "badge-ok"},
	attempt.TimedOut:   {"Timed out", "badge-muted"},
}

// attemptSort is the list order: key started|name|score, desc reversing the
// key only (ties stay in start order).
type attemptSort struct {
	key  string
	desc bool
}

func parseSort(q url.Values) attemptSort {
	by := attemptSort{key: "started"}
	if k := q.Get("sort"); k == "name" || k == "score" {
		by.key = k
	}
	by.desc = q.Get("dir") == "desc"
	return by
}

// sortAttempts orders rows, which arrive in start order, by the sort; the
// stable sort keeps ties in start order.
func sortAttempts(rows []attempt.Summary, by attemptSort) {
	var primary func(a, b attempt.Summary) int
	switch by.key {
	case "name":
		primary = func(a, b attempt.Summary) int {
			return strings.Compare(strings.ToLower(a.StudentName), strings.ToLower(b.StudentName))
		}
	case "score":
		primary = func(a, b attempt.Summary) int { return cmp.Compare(a.Score, b.Score) }
	default:
		primary = func(a, b attempt.Summary) int { return a.StartedAt.Compare(b.StartedAt) }
	}
	slices.SortStableFunc(rows, func(a, b attempt.Summary) int {
		if by.desc {
			return primary(b, a)
		}
		return primary(a, b)
	})
}

// sortMark is the aria-sort value and arrow of a column; empty when the list
// is not sorted by it.
type sortMark struct{ AriaSort, Arrow string }

func (by attemptSort) mark(key string) sortMark {
	switch {
	case by.key != key:
		return sortMark{}
	case by.desc:
		return sortMark{"descending", "▼"}
	default:
		return sortMark{"ascending", "▲"}
	}
}

type attemptRow struct {
	ID                int64
	Name              string
	Started, Finished string
	Status            statusLabel
	Score, Total      int
	TimeLeft          string // in progress only
	ResumeCode        string // in progress only
	InProgress        bool
}

type attemptsData struct {
	adminPage
	Exam                 exam.Exam
	Rows                 []attemptRow
	Counts               string
	Zone                 string
	NameHref, ScoreHref  string
	Started, Name, Score sortMark
}

func (s *server) attemptList(w http.ResponseWriter, r *http.Request) {
	e, ok := s.resultsExam(w, r)
	if !ok {
		return
	}
	rows, err := s.deps.Attempts.List(r.Context(), e.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	by := parseSort(r.URL.Query())
	sortAttempts(rows, by)

	data := attemptsData{
		adminPage: adminPageFor(r),
		Exam:      e,
		Rows:      make([]attemptRow, len(rows)),
		Counts:    countsLine(rows),
		Zone:      s.deps.Location.String(),
		NameHref:  "?sort=name&dir=asc",
		ScoreHref: "?sort=score&dir=desc",
		Started:   by.mark("started"),
		Name:      by.mark("name"),
		Score:     by.mark("score"),
	}
	if by.key == "name" && !by.desc {
		data.NameHref = "?sort=name&dir=desc"
	}
	if by.key == "score" && by.desc {
		data.ScoreHref = "?sort=score&dir=asc"
	}
	for i, sum := range rows {
		row := attemptRow{
			ID:      sum.ID,
			Name:    sum.StudentName,
			Started: s.listTime(sum.StartedAt),
			Status:  attemptStatus[sum.Status],
			Score:   sum.Score,
			Total:   sum.Total,
		}
		if sum.Status == attempt.InProgress {
			row.InProgress = true
			row.TimeLeft = clock(sum.Remaining)
			row.ResumeCode = sum.ResumeCode
		} else {
			row.Finished = s.listTime(sum.FinishedAt)
		}
		data.Rows[i] = row
	}
	s.page(w, r, http.StatusOK, "admin_attempts.html", data)
}

func (s *server) listTime(t time.Time) string {
	return t.In(s.deps.Location).Format(listTimeLayout)
}

// clock formats d as MM:SS in whole seconds, rounded down.
func clock(d time.Duration) string {
	secs := int(d / time.Second)
	return fmt.Sprintf("%02d:%02d", secs/60, secs%60)
}

// countsLine is "23 attempts — 5 in progress, 17 submitted, 1 timed out".
func countsLine(rows []attempt.Summary) string {
	n := map[attempt.Status]int{}
	for _, sum := range rows {
		n[sum.Status]++
	}
	noun := "attempts"
	if len(rows) == 1 {
		noun = "attempt"
	}
	return fmt.Sprintf("%d %s — %d in progress, %d submitted, %d timed out",
		len(rows), noun, n[attempt.InProgress], n[attempt.Submitted], n[attempt.TimedOut])
}

var csvStatus = map[attempt.Status]string{
	attempt.InProgress: "in progress",
	attempt.Submitted:  "submitted",
	attempt.TimedOut:   "timed out",
}

// attemptsCSV exports every attempt of the exam in start order.
func (s *server) attemptsCSV(w http.ResponseWriter, r *http.Request) {
	e, ok := s.resultsExam(w, r)
	if !ok {
		return
	}
	rows, err := s.deps.Attempts.List(r.Context(), e.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	cw.Write([]string{"name", "status", "score", "total", "started", "finished"})
	for _, sum := range rows {
		finished := ""
		if sum.Status != attempt.InProgress {
			finished = s.csvTime(sum.FinishedAt)
		}
		cw.Write([]string{
			csvName(sum.StudentName), csvStatus[sum.Status],
			strconv.Itoa(sum.Score), strconv.Itoa(sum.Total),
			s.csvTime(sum.StartedAt), finished,
		})
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="exam-%d-attempts.csv"`, e.ID))
	w.Write(buf.Bytes())
}

func (s *server) csvTime(t time.Time) string {
	return t.In(s.deps.Location).Format(time.RFC3339)
}

// csvName keeps a spreadsheet from reading a student name as a formula.
func csvName(name string) string {
	if name != "" && strings.ContainsRune("=+-@\t\r", rune(name[0])) {
		return "'" + name
	}
	return name
}

// resultsAttempt resolves the {id} exam and loads its {aid} attempt. On
// failure it has responded (404 or 500) and returns ok false.
func (s *server) resultsAttempt(w http.ResponseWriter, r *http.Request) (exam.Exam, attempt.Detail, bool) {
	e, ok := s.resultsExam(w, r)
	if !ok {
		return exam.Exam{}, attempt.Detail{}, false
	}
	aid, err := strconv.ParseInt(r.PathValue("aid"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return exam.Exam{}, attempt.Detail{}, false
	}
	d, err := s.deps.Attempts.Detail(r.Context(), e.ID, aid)
	if errors.Is(err, attempt.ErrNotFound) {
		s.notFound(w, r)
		return exam.Exam{}, attempt.Detail{}, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return exam.Exam{}, attempt.Detail{}, false
	}
	return e, d, true
}

type attemptDetailData struct {
	adminPage
	Exam              exam.Exam
	Attempt           attempt.Detail
	Status            statusLabel
	Started, Finished string
	Zone              string
}

func (s *server) attemptDetail(w http.ResponseWriter, r *http.Request) {
	e, d, ok := s.resultsAttempt(w, r)
	if !ok {
		return
	}
	data := attemptDetailData{
		adminPage: adminPageFor(r),
		Exam:      e,
		Attempt:   d,
		Status:    attemptStatus[d.Status],
		Started:   s.listTime(d.StartedAt),
		Finished:  "—",
		Zone:      s.deps.Location.String(),
	}
	if d.Status != attempt.InProgress {
		data.Finished = s.listTime(d.FinishedAt)
	}
	s.page(w, r, http.StatusOK, "admin_attempt.html", data)
}

type attemptDeleteData struct {
	adminPage
	Exam    exam.Exam
	Attempt attempt.Detail
}

func (s *server) attemptDeleteConfirm(w http.ResponseWriter, r *http.Request) {
	e, d, ok := s.resultsAttempt(w, r)
	if !ok {
		return
	}
	s.page(w, r, http.StatusOK, "admin_attempt_delete.html", attemptDeleteData{adminPage: adminPageFor(r), Exam: e, Attempt: d})
}

func (s *server) attemptDelete(w http.ResponseWriter, r *http.Request) {
	e, ok := s.resultsExam(w, r)
	if !ok {
		return
	}
	aid, err := strconv.ParseInt(r.PathValue("aid"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return
	}
	err = s.deps.Attempts.Delete(r.Context(), e.ID, aid)
	switch {
	case errors.Is(err, attempt.ErrNotFound):
		s.notFound(w, r)
	case err != nil:
		s.serverError(w, r, err)
	default:
		http.Redirect(w, r, attemptsPath(e.ID), http.StatusSeeOther)
	}
}
