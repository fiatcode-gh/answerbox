package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/exam"
	"github.com/fiatcode-gh/go-exam/internal/platform/sqlite"
)

// runCheck runs the command with args and returns its exit status and output.
func runCheck(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// currentCopy builds a current database with one imported exam and, when
// broken, one hand-inserted exam the rebuild cannot convert; it closes the
// database and returns the path of a copy in a fresh folder.
func currentCopy(t *testing.T, broken bool) string {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sqlite.Open(filepath.Join(dir, "go-exam.db"))
	if err != nil {
		t.Fatal(err)
	}
	media, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()
	res, err := db.Exec(`INSERT INTO teacher (email, created_at) VALUES ('owner@example.test', 0)`)
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := res.LastInsertId()
	exams := exam.NewService(db, media, time.Now, rand.Reader)
	if _, err := exams.Import(ctx, exam.Actor{TeacherID: owner}, []byte("# Simple\n\n1. Q one\n- a *\n- b\n")); err != nil {
		t.Fatal(err)
	}
	if broken {
		for _, q := range []string{
			fmt.Sprintf(`INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at, monitor, owner_id, code)
				VALUES (2,'List','none','score',0,'k2',5,1,%d,'GHJKMN')`, owner),
			`INSERT INTO question (exam_id, position, body_html) VALUES (2,1,'<ul>
<li>x</li>
</ul>
')`,
		} {
			if _, err := db.Exec(q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "go-exam.db"))
	if err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(t.TempDir(), "copy.db")
	if err := os.WriteFile(cp, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return cp
}

func TestRun_ExactAndFailed(t *testing.T) {
	code, out, errOut := runCheck("-db", currentCopy(t, true))
	want := `exam 1 "Simple": 1 questions, 2 options: ok
exam 2 "List": FAILED question 1: unsupported markup
1 of 2 exams rebuild exactly
`
	if code != 1 || out != want {
		t.Errorf("exit %d, stdout:\n%s\nstderr: %s\nwant exit 1 and\n%s", code, out, errOut, want)
	}

	code, out, errOut = runCheck("-db", currentCopy(t, false))
	if code != 0 || !strings.HasSuffix(out, "\n1 of 1 exams rebuild exactly\n") {
		t.Errorf("exit %d, stdout:\n%s\nstderr: %s\nwant exit 0 and 1 of 1", code, out, errOut)
	}
}

// oldDatabase writes a database file at path with the first version
// migrations applied, as that release left it, holding rows.
func oldDatabase(t *testing.T, path string, version int, rows ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	names, err := filepath.Glob("../../internal/platform/sqlite/migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names[:version] {
		schema, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(schema)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for _, q := range append([]string{fmt.Sprintf(`PRAGMA user_version = %d`, version)}, rows...) {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestRun_Version6Copy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "copy.db")
	oldDatabase(t, path, 6,
		`INSERT INTO teacher (id, email, created_at) VALUES (1,'first@example.test',0)`,
		`INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at, monitor, owner_id, code)
			VALUES (1,'Olimpiade','questions_options','score',1,'k1',5,1,1,'3VK583')`,
		`INSERT INTO question (id, exam_id, position, body_html) VALUES (1,1,1,'<p>Berapa 1 + 1?</p>
'),(2,1,2,'<p>Berapa 2 + 2?</p>
')`,
		`INSERT INTO question_option (question_id, position, body_html, is_correct) VALUES
			(1,1,'<p>2</p>
',1),(1,2,'<p>3</p>
',0),(2,1,'<p>4</p>
',1),(2,2,'<p>5</p>
',0)`,
	)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	names := dirNames(t, dir)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	code, out, errOut := runCheck("-db", path)
	if code != 0 || out != "exam 1 \"Olimpiade\": 2 questions, 4 options: ok\n1 of 1 exams rebuild exactly\n" {
		t.Errorf("exit %d, stdout:\n%s\nstderr: %s", code, out, errOut)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(after) != sha256.Sum256(before) {
		t.Error("the database file changed")
	}
	if v := binary.BigEndian.Uint32(after[60:64]); v != 6 {
		t.Errorf("user_version = %d, want 6", v)
	}
	if got := dirNames(t, dir); !slices.Equal(got, names) {
		t.Errorf("folder holds %v, want %v", got, names)
	}
}

func TestRun_Refusals(t *testing.T) {
	dir := t.TempDir()
	withWAL := filepath.Join(dir, "wal.db")
	oldDatabase(t, withWAL, 6)
	if err := os.WriteFile(withWAL+"-wal", []byte("frames"), 0o600); err != nil {
		t.Fatal(err)
	}
	v5 := filepath.Join(dir, "v5.db")
	oldDatabase(t, v5, 5)

	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"no -db", nil, "-db"},
		{"missing file", []string{"-db", filepath.Join(dir, "none.db")}, "none.db"},
		{"write-ahead log", []string{"-db", withWAL}, `the copy has a write-ahead log; take it with sqlite3 ".backup"`},
		{"version 5", []string{"-db", v5}, "version 5"},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, out, errOut := runCheck(c.args...)
			if code != 2 || !strings.Contains(errOut, c.want) || out != "" {
				t.Errorf("exit %d, stdout %q, stderr %q; want exit 2 and a message with %q", code, out, errOut, c.want)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(dir, "none.db")); !os.IsNotExist(err) {
		t.Errorf("missing file was created: %v", err)
	}
}

// The checker opens production copies; it must not carry the web server or
// the fake sign-in provider.
func TestBinary_DoesNotLinkWebOrFakeProvider(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	for line := range strings.Lines(string(out)) {
		if strings.Contains(line, "internal/oidcfake") || strings.Contains(line, "internal/web") {
			t.Errorf("cmd/rebuildcheck links %s", strings.TrimSpace(line))
		}
	}
}
