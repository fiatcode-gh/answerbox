// Command loadtest drives a running go-exam server with simulated students:
// the teacher imports and opens a generated exam, every student starts,
// answers all questions at once with the others, submits and checks the
// score, and the teacher's CSV export is verified against the expected scores.
//
// Opening the generated exam closes whatever exam is open, so the target must
// be a local server unless -allow-remote is given.
package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	adminCookieName   = "goexam_admin"
	attemptCookieName = "goexam_attempt"
	optionsPerQ       = 4
)

var (
	csrfRe  = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)
	radioRe = regexp.MustCompile(`<input type="radio" name="q(\d+)" value="(\d+)"[^>]*?( checked)?>`)
	scoreRe = regexp.MustCompile(`<p id="score">(\d+) / (\d+)</p>`)
	examRe  = regexp.MustCompile(`^/admin/exams/(\d+)$`)
)

type config struct {
	baseURL   string
	password  string
	students  int
	questions int
	timeout   time.Duration
}

func main() {
	var cfg config
	var allowRemote bool
	flag.StringVar(&cfg.baseURL, "base-url", "http://localhost:18080", "go-exam server base URL")
	flag.StringVar(&cfg.password, "admin-password", os.Getenv("GOEXAM_ADMIN_PASSWORD"), "teacher password (default: $GOEXAM_ADMIN_PASSWORD)")
	flag.IntVar(&cfg.students, "students", 100, "number of simulated students")
	flag.IntVar(&cfg.questions, "questions", 40, "number of questions in the generated exam")
	flag.DurationVar(&cfg.timeout, "timeout", 2*time.Minute, "deadline for the whole run")
	flag.BoolVar(&allowRemote, "allow-remote", false, "allow a base URL that is not localhost (opening the exam closes the open one)")
	flag.Parse()

	if err := checkConfig(cfg, allowRemote); err != nil {
		fmt.Fprintln(os.Stderr, "loadtest:", err)
		os.Exit(2)
	}
	cfg.baseURL = strings.TrimRight(cfg.baseURL, "/")

	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()
	if !run(ctx, cfg) {
		os.Exit(1)
	}
}

func checkConfig(cfg config, allowRemote bool) error {
	u, err := url.Parse(cfg.baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("invalid -base-url %q", cfg.baseURL)
	}
	switch host := u.Hostname(); {
	case host == "localhost", host == "127.0.0.1", host == "::1":
	case allowRemote:
	default:
		return fmt.Errorf("refusing non-local host %q: opening the load-test exam closes the open exam (use -allow-remote)", host)
	}
	if cfg.password == "" {
		return errors.New("no admin password: set -admin-password or GOEXAM_ADMIN_PASSWORD")
	}
	if cfg.students < 1 || cfg.questions < 1 {
		return errors.New("-students and -questions must be at least 1")
	}
	return nil
}

// tally collects errors (failed or unexpected HTTP exchanges) and mismatches
// (well-formed responses whose content contradicts the expected outcome).
type tally struct {
	mu         sync.Mutex
	errors     int
	mismatches int
}

// fail reports an error for student (0 = teacher) at step.
func (t *tally) fail(student int, step string, format string, args ...any) {
	t.mu.Lock()
	t.errors++
	t.mu.Unlock()
	fmt.Fprintf(os.Stderr, "ERROR %s step=%s: %s\n", who(student), step, fmt.Sprintf(format, args...))
}

func (t *tally) mismatch(student int, step string, format string, args ...any) {
	t.mu.Lock()
	t.mismatches++
	t.mu.Unlock()
	fmt.Fprintf(os.Stderr, "MISMATCH %s step=%s: %s\n", who(student), step, fmt.Sprintf(format, args...))
}

func who(student int) string {
	if student == 0 {
		return "teacher"
	}
	return fmt.Sprintf("student=%d", student)
}

type client struct {
	http *http.Client
	base string
}

// do sends one request with the given cookie ("name=value", may be empty) and
// returns the response with its body read. No retries.
func (c *client) do(ctx context.Context, method, path, cookie, contentType string, body []byte) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return resp, data, err
}

func (c *client) postForm(ctx context.Context, path, cookie string, form url.Values) (*http.Response, []byte, error) {
	return c.do(ctx, http.MethodPost, path, cookie, "application/x-www-form-urlencoded", []byte(form.Encode()))
}

// cookieFrom returns "name=value" for the named Set-Cookie of resp, or "".
func cookieFrom(resp *http.Response, name string) string {
	for _, c := range resp.Cookies() {
		if c.Name == name && c.Value != "" {
			return c.Name + "=" + c.Value
		}
	}
	return ""
}

func expectRedirect(resp *http.Response, location string) error {
	if resp.StatusCode != http.StatusSeeOther {
		return fmt.Errorf("status %d, want 303", resp.StatusCode)
	}
	if location != "" && resp.Header.Get("Location") != location {
		return fmt.Errorf("status 303 to %q, want %q", resp.Header.Get("Location"), location)
	}
	return nil
}

func run(ctx context.Context, cfg config) bool {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = cfg.students
	c := &client{
		http: &http.Client{
			Transport:     transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		base: cfg.baseURL,
	}
	t := &tally{}

	admin, examID, err := setup(ctx, c, cfg)
	if err != nil {
		t.fail(0, "setup", "%v", err)
		report(cfg, t, nil, 0)
		return false
	}

	var ready sync.WaitGroup
	ready.Add(cfg.students)
	start := make(chan struct{})
	latencies := make([][]time.Duration, cfg.students+1)
	var done sync.WaitGroup
	for i := 1; i <= cfg.students; i++ {
		done.Go(func() {
			latencies[i] = student(ctx, c, cfg, t, i, &ready, start)
		})
	}
	ready.Wait()
	began := time.Now()
	close(start)
	done.Wait()
	wall := time.Since(began)

	verifyCSV(ctx, c, cfg, t, admin, examID)

	report(cfg, t, slices.Concat(latencies...), wall)
	return t.errors == 0 && t.mismatches == 0
}

func setup(ctx context.Context, c *client, cfg config) (admin string, examID int, err error) {
	resp, _, err := c.postForm(ctx, "/admin/login", "", url.Values{"password": {cfg.password}})
	if err != nil {
		return "", 0, fmt.Errorf("login: %w", err)
	}
	if err := expectRedirect(resp, "/admin/exams"); err != nil {
		return "", 0, fmt.Errorf("login: %w", err)
	}
	if admin = cookieFrom(resp, adminCookieName); admin == "" {
		return "", 0, errors.New("login: no admin cookie")
	}

	resp, body, err := c.do(ctx, http.MethodGet, "/admin/exams", admin, "", nil)
	if err != nil {
		return "", 0, fmt.Errorf("exams page: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("exams page: status %d", resp.StatusCode)
	}
	m := csrfRe.FindSubmatch(body)
	if m == nil {
		return "", 0, errors.New("exams page: no csrf_token field")
	}
	csrf := string(m[1])

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("csrf_token", csrf)
	fw, err := mw.CreateFormFile("file", "loadtest.md")
	if err != nil {
		return "", 0, err
	}
	fw.Write(examText(cfg.questions, time.Now()))
	mw.Close()
	resp, _, err = c.do(ctx, http.MethodPost, "/admin/exams", admin, mw.FormDataContentType(), buf.Bytes())
	if err != nil {
		return "", 0, fmt.Errorf("import: %w", err)
	}
	if err := expectRedirect(resp, ""); err != nil {
		return "", 0, fmt.Errorf("import: %w", err)
	}
	loc := examRe.FindStringSubmatch(resp.Header.Get("Location"))
	if loc == nil {
		return "", 0, fmt.Errorf("import: redirect to %q, want /admin/exams/<id>", resp.Header.Get("Location"))
	}
	examID, _ = strconv.Atoi(loc[1])

	resp, _, err = c.postForm(ctx, fmt.Sprintf("/admin/exams/%d/open", examID), admin, url.Values{"csrf_token": {csrf}})
	if err != nil {
		return "", 0, fmt.Errorf("open: %w", err)
	}
	if err := expectRedirect(resp, "/admin/exams"); err != nil {
		return "", 0, fmt.Errorf("open: %w", err)
	}
	return admin, examID, nil
}

// examText generates the exam file. The correct option of question q
// (1-based) is option correctOption(q-1)+1.
func examText(questions int, now time.Time) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Load test %s\nshuffle: questions, options\nshow_result: score\n\n", now.Format(time.RFC3339))
	for q := 1; q <= questions; q++ {
		fmt.Fprintf(&b, "%d. Question %d\n", q, q)
		for k := 1; k <= optionsPerQ; k++ {
			mark := ""
			if k-1 == correctOption(q-1) {
				mark = " *"
			}
			fmt.Fprintf(&b, "- Q%d option %d%s\n", q, k, mark)
		}
		b.WriteString("\n")
	}
	return []byte(b.String())
}

// correctOption is the 0-based correct option of 0-based file question q.
func correctOption(q int) int { return q % optionsPerQ }

// expectedScore is the score student i aims for: the first expectedScore(i)
// file questions are answered correctly, the rest with the next option.
func expectedScore(i, questions int) int { return i % (questions + 1) }

type pick struct{ question, option string }

func student(ctx context.Context, c *client, cfg config, t *tally, i int, ready *sync.WaitGroup, start <-chan struct{}) []time.Duration {
	picks, cookie := prepare(ctx, c, cfg, t, i)
	ready.Done()
	<-start
	if picks == nil {
		return nil
	}

	latencies := make([]time.Duration, 0, len(picks))
	for _, p := range picks {
		began := time.Now()
		resp, body, err := c.postForm(ctx, "/exam/answer", cookie, url.Values{"question": {p.question}, "option": {p.option}})
		if err != nil {
			t.fail(i, "answer", "%v", err)
			return latencies
		}
		latencies = append(latencies, time.Since(began))
		var saved struct {
			Saved bool `json:"saved"`
		}
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &saved) != nil || !saved.Saved {
			t.fail(i, "answer", "status %d, body %q", resp.StatusCode, body)
			return latencies
		}
	}

	resp, body, err := c.do(ctx, http.MethodGet, "/exam", cookie, "", nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.fail(i, "recheck", "%v", statusErr(resp, err))
		return latencies
	}
	checked := 0
	for _, m := range radioRe.FindAllSubmatch(body, -1) {
		if len(m[3]) > 0 {
			checked++
		}
	}
	if checked != cfg.questions {
		t.mismatch(i, "recheck", "%d checked radios, want %d", checked, cfg.questions)
	}

	resp, _, err = c.postForm(ctx, "/exam/submit", cookie, nil)
	if err != nil {
		t.fail(i, "submit", "%v", err)
		return latencies
	}
	if err := expectRedirect(resp, "/exam"); err != nil {
		t.fail(i, "submit", "%v", err)
		return latencies
	}

	resp, body, err = c.do(ctx, http.MethodGet, "/exam", cookie, "", nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.fail(i, "result", "%v", statusErr(resp, err))
		return latencies
	}
	want := fmt.Sprintf("%d / %d", expectedScore(i, cfg.questions), cfg.questions)
	if m := scoreRe.FindSubmatch(body); m == nil {
		t.mismatch(i, "result", "no score on result page")
	} else if got := string(m[1]) + " / " + string(m[2]); got != want {
		t.mismatch(i, "result", "score %s, want %s", got, want)
	}
	return latencies
}

// prepare starts student i and plans its answers in display order. It returns
// nil picks after reporting a failure.
func prepare(ctx context.Context, c *client, cfg config, t *tally, i int) ([]pick, string) {
	resp, _, err := c.postForm(ctx, "/start", "", url.Values{"name": {fmt.Sprintf("Student %03d", i)}})
	if err != nil {
		t.fail(i, "start", "%v", err)
		return nil, ""
	}
	if err := expectRedirect(resp, "/exam"); err != nil {
		t.fail(i, "start", "%v", err)
		return nil, ""
	}
	cookie := cookieFrom(resp, attemptCookieName)
	if cookie == "" {
		t.fail(i, "start", "status %d without attempt cookie", resp.StatusCode)
		return nil, ""
	}

	resp, body, err := c.do(ctx, http.MethodGet, "/exam", cookie, "", nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.fail(i, "exam", "%v", statusErr(resp, err))
		return nil, ""
	}
	var display []int          // question ids in display order
	options := map[int][]int{} // question id → option ids
	for _, m := range radioRe.FindAllSubmatch(body, -1) {
		qid, _ := strconv.Atoi(string(m[1]))
		oid, _ := strconv.Atoi(string(m[2]))
		if _, seen := options[qid]; !seen {
			display = append(display, qid)
		}
		options[qid] = append(options[qid], oid)
	}
	if len(display) != cfg.questions {
		t.fail(i, "exam", "page has %d questions, want %d", len(display), cfg.questions)
		return nil, ""
	}
	for _, qid := range display {
		if len(options[qid]) != optionsPerQ {
			t.fail(i, "exam", "question %d has %d options, want %d", qid, len(options[qid]), optionsPerQ)
			return nil, ""
		}
	}

	// Imports insert in file order, so ascending ids recover file order.
	fileOrder := slices.Sorted(slices.Values(display))
	choice := map[int]int{}
	score := expectedScore(i, cfg.questions)
	for q, qid := range fileOrder {
		opts := slices.Sorted(slices.Values(options[qid]))
		k := correctOption(q)
		if q >= score {
			k = (k + 1) % optionsPerQ
		}
		choice[qid] = opts[k]
	}
	picks := make([]pick, 0, len(display))
	for _, qid := range display {
		picks = append(picks, pick{strconv.Itoa(qid), strconv.Itoa(choice[qid])})
	}
	return picks, cookie
}

func statusErr(resp *http.Response, err error) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("status %d", resp.StatusCode)
}

func verifyCSV(ctx context.Context, c *client, cfg config, t *tally, admin string, examID int) {
	resp, body, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/admin/exams/%d/attempts.csv", examID), admin, "", nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.fail(0, "csv", "%v", statusErr(resp, err))
		return
	}
	records, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil || len(records) == 0 {
		t.fail(0, "csv", "unreadable export: %v", err)
		return
	}
	col := map[string]int{}
	for k, name := range records[0] {
		col[name] = k
	}
	for _, name := range []string{"name", "status", "score", "total"} {
		if _, ok := col[name]; !ok {
			t.fail(0, "csv", "no %q column", name)
			return
		}
	}
	rows := records[1:]
	if len(rows) != cfg.students {
		t.mismatch(0, "csv", "%d data rows, want %d", len(rows), cfg.students)
	}
	seen := map[int]bool{}
	for _, row := range rows {
		name := row[col["name"]]
		var i int
		if _, err := fmt.Sscanf(name, "Student %d", &i); err != nil || i < 1 || i > cfg.students || seen[i] {
			t.mismatch(0, "csv", "unexpected or duplicate row %q", name)
			continue
		}
		seen[i] = true
		want := []string{"submitted", strconv.Itoa(expectedScore(i, cfg.questions)), strconv.Itoa(cfg.questions)}
		got := []string{row[col["status"]], row[col["score"]], row[col["total"]]}
		if !slices.Equal(got, want) {
			t.mismatch(i, "csv", "status/score/total %v, want %v", got, want)
		}
	}
}

func report(cfg config, t *tally, latencies []time.Duration, wall time.Duration) {
	slices.Sort(latencies)
	fmt.Printf("students=%d questions=%d saves=%d errors=%d mismatches=%d\n",
		cfg.students, cfg.questions, len(latencies), t.errors, t.mismatches)
	fmt.Printf("save latency ms: p50=%s p95=%s p99=%s max=%s\n",
		ms(percentile(latencies, 50)), ms(percentile(latencies, 95)), ms(percentile(latencies, 99)), ms(percentile(latencies, 100)))
	rate := 0.0
	if wall > 0 {
		rate = float64(len(latencies)) / wall.Seconds()
	}
	fmt.Printf("wall=%.2fs saves/s=%.0f\n", wall.Seconds(), rate)
}

// percentile uses the nearest-rank method on sorted durations.
func percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := (p*len(sorted) + 99) / 100
	return sorted[max(rank, 1)-1]
}

func ms(d time.Duration) string {
	return strconv.FormatFloat(float64(d)/float64(time.Millisecond), 'f', 1, 64)
}
