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
	"github.com/fiatcode-gh/go-exam/internal/examfile"
)

// mountTeacherResults registers the results pages on the admin sub-mux.
func (s *server) mountTeacherResults(admin *http.ServeMux) {
	admin.HandleFunc("GET /admin/exams/{id}/attempts", s.attemptList)
	admin.HandleFunc("GET /admin/exams/{id}/attempts.csv", s.attemptsCSV)
	admin.HandleFunc("GET /admin/exams/{id}/attempts/{aid}", s.attemptDetail)
	admin.HandleFunc("GET /admin/exams/{id}/attempts/{aid}/delete", s.attemptDeleteConfirm)
	admin.HandleFunc("POST /admin/exams/{id}/attempts/{aid}/delete", s.attemptDelete)
	admin.HandleFunc("POST /admin/exams/{id}/attempts/{aid}/rename", s.attemptRename)
}

// resultsExam resolves the {id} exam of a results route for the signed-in
// teacher; every results handler calls it before touching attempts. On
// failure it has responded (404 or 500) and returns ok false.
func (s *server) resultsExam(w http.ResponseWriter, r *http.Request) (e exam.Exam, ok bool) {
	id, ok := examID(r)
	if !ok {
		s.notFound(w, r)
		return exam.Exam{}, false
	}
	e, err := s.deps.Exams.Get(r.Context(), actorFrom(r), id)
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

func attemptPath(examID, attemptID int64) string {
	return attemptsPath(examID) + "/" + strconv.FormatInt(attemptID, 10)
}

// attemptID parses the {aid} path value; ok is false when it is not a number.
func attemptID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("aid"), 10, 64)
	return id, err == nil
}

const (
	clockLayout    = "15:04"            // a time in the attempts table
	dayLayout      = "2 Jan"            // put before a clock on another date
	dateLayout     = "Mon, 2 Jan 2006"  // the shared start date above the table
	fullTimeLayout = "2006-01-02 15:04" // tooltips and the attempt page
)

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

// dir is the sort direction as its query value.
func (by attemptSort) dir() string {
	if by.desc {
		return "desc"
	}
	return "asc"
}

// listQuery is the attempts-list query for a sort and a name filter.
func listQuery(key, dir, name string) string {
	q := "?sort=" + key + "&dir=" + dir
	if name != "" {
		q += "&name=" + url.QueryEscape(name)
	}
	return q
}

// filterByName keeps the rows whose student name contains name, ignoring
// case; an empty name keeps all. It narrows rows in place.
func filterByName(rows []attempt.Summary, name string) []attempt.Summary {
	if name == "" {
		return rows
	}
	needle := strings.ToLower(name)
	return slices.DeleteFunc(rows, func(sum attempt.Summary) bool {
		return !strings.Contains(strings.ToLower(sum.StudentName), needle)
	})
}

// timeCell is a time in the attempts table: Text is shown, Full is its tooltip.
type timeCell struct{ Text, Full string }

// sameDay reports whether a and b fall on one calendar date in loc.
func sameDay(a, b time.Time, loc *time.Location) bool {
	ay, am, ad := a.In(loc).Date()
	by, bm, bd := b.In(loc).Date()
	return ay == by && am == bm && ad == bd
}

// listTimes formats the attempts table's times in loc. When every shown
// attempt started on one date, shared is true and day is one of those starts:
// a time on that date shows the clock alone. Any other time carries its
// short date.
type listTimes struct {
	loc    *time.Location
	day    time.Time
	shared bool
}

func newListTimes(rows []attempt.Summary, loc *time.Location) listTimes {
	lt := listTimes{loc: loc, shared: len(rows) > 0}
	for _, r := range rows {
		if !sameDay(r.StartedAt, rows[0].StartedAt, loc) {
			lt.shared = false
			break
		}
	}
	if lt.shared {
		lt.day = rows[0].StartedAt
	}
	return lt
}

func (lt listTimes) cell(t time.Time) timeCell {
	local := t.In(lt.loc)
	c := timeCell{Text: local.Format(clockLayout), Full: local.Format(fullTimeLayout)}
	if !lt.shared || !sameDay(t, lt.day, lt.loc) {
		c.Text = local.Format(dayLayout) + " " + c.Text
	}
	return c
}

type attemptRow struct {
	ID                int64
	Name              string
	Started, Finished timeCell
	Status            statusLabel
	Score, Total      int
	TimeLeft          string // in progress only
	ResumeCode        string // in progress only
	InProgress        bool
	Flags             int
}

type attemptsData struct {
	adminPage
	Exam                 exam.Exam
	Rows                 []attemptRow
	Counts               string
	Total                int // attempts before the name filter
	Filter               string
	ClearHref            string
	SortKey, SortDir     string
	Date                 string // the start date shared by every shown attempt, if any
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
	total := len(rows)
	filter := strings.TrimSpace(r.URL.Query().Get("name"))
	rows = filterByName(rows, filter)
	by := parseSort(r.URL.Query())
	sortAttempts(rows, by)
	lt := newListTimes(rows, s.deps.Location)

	nameDir, scoreDir := "asc", "desc"
	if by.key == "name" && !by.desc {
		nameDir = "desc"
	}
	if by.key == "score" && by.desc {
		scoreDir = "asc"
	}
	data := attemptsData{
		adminPage: adminPageFor(r),
		Exam:      e,
		Rows:      make([]attemptRow, len(rows)),
		Counts:    countsLine(rows, total, filter != ""),
		Total:     total,
		Filter:    filter,
		ClearHref: listQuery(by.key, by.dir(), ""),
		SortKey:   by.key,
		SortDir:   by.dir(),
		Zone:      s.deps.Location.String(),
		NameHref:  listQuery("name", nameDir, filter),
		ScoreHref: listQuery("score", scoreDir, filter),
		Started:   by.mark("started"),
		Name:      by.mark("name"),
		Score:     by.mark("score"),
	}
	if lt.shared {
		data.Date = lt.day.In(s.deps.Location).Format(dateLayout)
	}
	for i, sum := range rows {
		row := attemptRow{
			ID:      sum.ID,
			Name:    sum.StudentName,
			Started: lt.cell(sum.StartedAt),
			Status:  attemptStatus[sum.Status],
			Score:   sum.Score,
			Total:   sum.Total,
			Flags:   sum.Flags,
		}
		if sum.Status == attempt.InProgress {
			row.InProgress = true
			row.TimeLeft = clock(sum.Remaining)
			row.ResumeCode = sum.ResumeCode
		} else {
			row.Finished = lt.cell(sum.FinishedAt)
		}
		data.Rows[i] = row
	}
	s.page(w, r, http.StatusOK, "admin_attempts.html", data)
}

// fullTime is t as date and time in the configured zone, without seconds.
func (s *server) fullTime(t time.Time) string {
	return t.In(s.deps.Location).Format(fullTimeLayout)
}

// clock formats d as MM:SS in whole seconds, rounded down.
func clock(d time.Duration) string {
	secs := int(d / time.Second)
	return fmt.Sprintf("%02d:%02d", secs/60, secs%60)
}

// countsLine is "23 attempts — 5 in progress, 17 submitted, 1 timed out" for
// the shown rows; when filtered, "3 of 23 attempts — …" against total.
func countsLine(rows []attempt.Summary, total int, filtered bool) string {
	n := map[attempt.Status]int{}
	for _, sum := range rows {
		n[sum.Status]++
	}
	noun := "attempts"
	if total == 1 {
		noun = "attempt"
	}
	count := fmt.Sprint(total)
	if filtered {
		count = fmt.Sprintf("%d of %d", len(rows), total)
	}
	return fmt.Sprintf("%s %s — %d in progress, %d submitted, %d timed out",
		count, noun, n[attempt.InProgress], n[attempt.Submitted], n[attempt.TimedOut])
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
	cw.Write([]string{"name", "status", "score", "total", "started", "finished", "flags"})
	for _, sum := range rows {
		finished := ""
		if sum.Status != attempt.InProgress {
			finished = s.csvTime(sum.FinishedAt)
		}
		cw.Write([]string{
			csvName(sum.StudentName), csvStatus[sum.Status],
			strconv.Itoa(sum.Score), strconv.Itoa(sum.Total),
			s.csvTime(sum.StartedAt), finished, strconv.Itoa(sum.Flags),
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
	aid, ok := attemptID(r)
	if !ok {
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

// renameAlert reports a refused rename on the attempt page.
const renameAlert = "Enter a name (1 to 100 characters)."

type attemptDetailData struct {
	adminPage
	Exam              exam.Exam
	Attempt           attempt.Detail
	Status            statusLabel
	Started, Finished string
	Zone              string
	Events            []eventRow
	RenameName        string // value of the rename field
	Alert             string
}

func (s *server) attemptDetail(w http.ResponseWriter, r *http.Request) {
	e, d, ok := s.resultsAttempt(w, r)
	if !ok {
		return
	}
	s.renderAttemptDetail(w, r, http.StatusOK, e, d, d.StudentName, "")
}

// renderAttemptDetail renders the attempt page. renameName fills the rename
// field; a non-empty alert reports a refused rename.
func (s *server) renderAttemptDetail(w http.ResponseWriter, r *http.Request, status int,
	e exam.Exam, d attempt.Detail, renameName, alert string) {
	data := attemptDetailData{
		adminPage:  adminPageFor(r),
		Exam:       e,
		Attempt:    d,
		Status:     attemptStatus[d.Status],
		Started:    s.fullTime(d.StartedAt),
		Finished:   "—",
		Zone:       s.deps.Location.String(),
		RenameName: renameName,
		Alert:      alert,
	}
	if d.Status != attempt.InProgress {
		data.Finished = s.fullTime(d.FinishedAt)
	}
	shuffled := e.Shuffle != examfile.ShuffleNone
	for _, ev := range d.Events {
		row := eventRow{Time: s.fullTime(ev.At), Text: eventText[ev.Kind], Question: ev.Question}
		if shuffled {
			row.Screen = ev.Screen
		}
		data.Events = append(data.Events, row)
	}
	s.page(w, r, status, "admin_attempt.html", data)
}

func (s *server) attemptRename(w http.ResponseWriter, r *http.Request) {
	e, ok := s.resultsExam(w, r)
	if !ok {
		return
	}
	aid, ok := attemptID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	name := r.PostFormValue("name")
	err := s.deps.Attempts.Rename(r.Context(), e.ID, aid, name)
	switch {
	case errors.Is(err, attempt.ErrInvalidName):
		if e, d, ok := s.resultsAttempt(w, r); ok {
			s.renderAttemptDetail(w, r, http.StatusUnprocessableEntity, e, d, name, renameAlert)
		}
	case errors.Is(err, attempt.ErrNotFound):
		s.notFound(w, r)
	case err != nil:
		s.serverError(w, r, err)
	default:
		http.Redirect(w, r, attemptPath(e.ID, aid), http.StatusSeeOther)
	}
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
	aid, ok := attemptID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	err := s.deps.Attempts.Delete(r.Context(), e.ID, aid)
	switch {
	case errors.Is(err, attempt.ErrNotFound):
		s.notFound(w, r)
	case err != nil:
		s.serverError(w, r, err)
	default:
		http.Redirect(w, r, attemptsPath(e.ID), http.StatusSeeOther)
	}
}
