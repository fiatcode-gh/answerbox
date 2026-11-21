# Development

## Checks

CI runs the first five on every push; run them before you commit.

```sh
gofmt -l .                                              # must print nothing
go vet ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...
go test ./...
go test -race ./...
podman build -t localhost/answerbox:dev -f Containerfile .
```

## Running locally

With your own settings in `.env` (never printed):

```sh
( set -a; . ./.env; set +a; exec go run ./cmd/answerbox )
```

Or against the fake OpenID provider, which signs in any identity and is not in the container image. The app must be reached as `localhost` and the provider as `127.0.0.1`:

```sh
go run ./cmd/oidcfake                                   # serves on 127.0.0.1:9096
ANSWERBOX_GOOGLE_CLIENT_ID=dev-client ANSWERBOX_GOOGLE_CLIENT_SECRET=dev-secret \
ANSWERBOX_ADMIN_EMAILS=admin@example.test ANSWERBOX_PUBLIC_URL=http://localhost:8080 \
ANSWERBOX_OIDC_ISSUER=http://127.0.0.1:9096 ANSWERBOX_DATA_DIR=./data go run ./cmd/answerbox
```

## Browser scripts

The scripts in `e2e/` drive Chromium through Playwright and run with `uv`. Each one expects a fresh data directory and takes the same arguments:

```sh
args="--base-url http://localhost:8080 --admin-email admin@example.test --oidc-url http://127.0.0.1:9096"
uv run e2e/student_ui.py $args --out e2e/out/student-ui
uv run e2e/walkthrough_id.py $args --out e2e/out/walkthrough-id
```

| Script | Covers |
| --- | --- |
| `student_ui.py` | the student exam page |
| `exam_flow.py` | import, start, answer, submit, results |
| `monitor.py` | the monitor's events and flags |
| `teachers.py` | sign-in, the teacher list, ownership |
| `exam_codes.py` | codes, links and refusals |
| `revisions.py` | revisions, History, Download, Copy |
| `editor.py` | the editor |
| `archive.py` | archive and unarchive |
| `sections.py` | sections |
| `fillin.py` | fill-in questions and overrides |
| `scheduled_exams.py` | schedules; the server must run with `ANSWERBOX_TIME_ZONE=Asia/Jakarta`, add `--time-zone Asia/Jakarta`; waits about two minutes |
| `walkthrough.py` | every teacher page without scripts, keyboard only, a 360 px phone, both colour schemes |
| `screenshots.py` | every page in both schemes at 412 and 1280 px; `--readme docs/screenshots` rewrites the README pictures (about six minutes) |
| `language.py` | the language switch: the first visit by browser language, every kind of page without scripts, the cookie over the header, and on the exam page saving before the switch with the monitor on |
| `walkthrough_id.py` | the app in Indonesian, students and teacher, at 360 and 1280 px in both colour schemes, with screenshots |

`editor.py`, `archive.py`, `sections.py` and `fillin.py` also take `--rollback` to rehearse running the previous release on a copy of the data they left; see the decision record for each rollback.

Apart from `language.py` and `walkthrough_id.py`, the scripts run with Playwright's default `en-US` locale, so they see the English pages.

## Other tools

```sh
# Load test: 100 students saving answers against a running instance
go run ./cmd/loadtest -base-url http://localhost:8080 -teacher-email admin@example.test

# Check that every exam in a database copy can be given its revision 1 (read-only; use a copy made with sqlite3 ".backup")
go run ./cmd/rebuildcheck -db /path/to/copy.db

# Print every interface text in English and Indonesian as one Markdown table, grouped by page, for a wording review
go test ./internal/web -run 'TestMessages_ReviewTable$' -v
```
