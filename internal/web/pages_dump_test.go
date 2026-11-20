package web

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestPages_EnglishDump writes the English body of every page-fixture case
// into the directory ANSWERBOX_PAGE_DUMP names, so two trees can be compared
// with diff -r. It is scaffolding for the localization tasks and is skipped
// without the variable.
func TestPages_EnglishDump(t *testing.T) {
	dir := os.Getenv("ANSWERBOX_PAGE_DUMP")
	if dir == "" {
		t.Skip("ANSWERBOX_PAGE_DUMP is not set")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	env, cases := pageCases(t)
	stable := runVarying(t, env)
	for _, c := range cases {
		body := stable.Replace(env.fetch(t, c, langEN))
		body = assetVersion.ReplaceAllString(body, "?v=VERSION")
		body = attrEntities.Replace(body)
		if err := os.WriteFile(filepath.Join(dir, c.name+".html"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("wrote %d pages to %s", len(cases), dir)
}

var assetVersion = regexp.MustCompile(`\?v=[^"'&\s]+`)

// attrEntities decodes what moving a text into an action can change in an
// attribute or <title> value.
var attrEntities = strings.NewReplacer("&#39;", "'", "&#34;", `"`, "&#43;", "+")

// runVarying replaces every value the fixture's state draws at random by a
// placeholder named after its row: exam codes (as text and as tiles), resume
// codes (stored and as shown, split by a hyphen), CSRF tokens and media keys.
func runVarying(t *testing.T, env *testEnv) *strings.Replacer {
	t.Helper()
	var pairs []string
	add := func(query, label string, tiles, hyphenated bool) {
		t.Helper()
		rows, err := env.db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var value sql.NullString
			if err := rows.Scan(&id, &value); err != nil {
				t.Fatal(err)
			}
			if !value.Valid || value.String == "" {
				continue
			}
			placeholder := label + "-" + id
			if tiles {
				var tile strings.Builder
				for _, r := range value.String {
					tile.WriteString("<span>" + string(r) + "</span>")
				}
				pairs = append(pairs, tile.String(), "<span>"+placeholder+"</span>")
			}
			if hyphenated && len(value.String) == 8 {
				pairs = append(pairs, value.String[:4]+"-"+value.String[4:], placeholder)
			}
			pairs = append(pairs, value.String, placeholder)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
	add(`SELECT id, code FROM exam`, "EXAMCODE", true, false)
	add(`SELECT id, resume_code FROM attempt`, "RESUMECODE", false, true)
	add(`SELECT teacher_id, csrf_token FROM admin_session`, "CSRF", false, false)
	add(`SELECT id, media_key FROM exam`, "MEDIA", false, false)
	add(`SELECT exam_id || '-' || number, media_key FROM exam_revision`, "MEDIA", false, false)
	return strings.NewReplacer(pairs...)
}
