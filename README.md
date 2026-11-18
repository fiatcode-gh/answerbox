# go-exam

go-exam is a small, self-hosted web app for classroom multiple-choice exams. Students type the exam's code and their name, or open a link that carries the code, and take that exam within its time limit, the exam's own length (60 minutes unless the exam file says otherwise); the teacher imports exams from text files or writes them in the browser, opens and closes them by hand or on a schedule, and reviews the results. Teachers can also see every change to an exam as a numbered revision, download an exam as a file, copy an exam and archive exams they no longer use.

It is not a learning platform. Teachers sign in with their Google account, and only emails on the app's teacher list may sign in. Each teacher sees only their own exams and results; admins (the emails in `GOEXAM_ADMIN_EMAILS`) also see every exam and manage the teacher list. Students have no accounts. Several exams can be open at the same time.

## Configuration

All configuration comes from environment variables.

| Variable | Default | Meaning |
| --- | --- | --- |
| `GOEXAM_GOOGLE_CLIENT_ID` | none (required) | OAuth client ID of the Google Cloud "Web application" client. |
| `GOEXAM_GOOGLE_CLIENT_SECRET` | none (required) | Client secret of that OAuth client. |
| `GOEXAM_ADMIN_EMAILS` | none (required) | Comma-separated Google account emails of the admins. The first one receives the exams that existed before teacher accounts. |
| `GOEXAM_PUBLIC_URL` | none (required) | Public address of the app, scheme and host without a path, for example `https://exam.example.org`; `https` unless the host is localhost. The student link shown to teachers starts with it. |
| `GOEXAM_OIDC_ISSUER` | `https://accounts.google.com` | OpenID provider; `https` unless the host is localhost. Set it only for another provider or the local fake used in tests. |
| `GOEXAM_DATA_DIR` | `./data` (image: `/data`) | Directory holding the database and uploaded images. |
| `GOEXAM_LISTEN_ADDR` | `:8080` | Address the HTTP server listens on. |
| `GOEXAM_TRUSTED_PROXIES` | empty (trust nobody) | Comma-separated CIDRs or IPs whose `X-Forwarded-For` is trusted for the client address in the request log. |
| `GOEXAM_TIME_ZONE` | `UTC` | IANA time zone used to display times, to type and show schedule times, and for the open time students read (time zone data is built in). |

The app refuses to start without the four required settings. [`.env.example`](.env.example) lists every setting with a one-line explanation; the optional ones stay commented out, because an empty value in an env file overrides the default (for example the image's `/data`). Teacher sessions last 12 hours and survive restarts. The admin role follows `GOEXAM_ADMIN_EMAILS` at every request; an email taken out of it stays an ordinary teacher until an admin removes it on the Teachers page. Recovery is an environment change plus a restart: there is no password.

## Google sign-in

1. In Google Cloud, create an OAuth client of type "Web application".
2. Add the authorised redirect URI `<GOEXAM_PUBLIC_URL>/admin/auth/callback`, for example `https://exam.example.org/admin/auth/callback`.
3. The app asks for the `openid`, `email` and `profile` scopes.
4. Put the client ID and secret in `GOEXAM_GOOGLE_CLIENT_ID` and `GOEXAM_GOOGLE_CLIENT_SECRET`.
5. The first admin signs in at `/admin` and adds teachers on the Teachers page.

A Google account can sign in only when its email is verified and on the teacher list.

## Exam codes

- Every exam gets a 6-character code when it is imported: capital letters and digits without look-alikes (no I, L, O, U, 0, 1). The code never changes, also when the questions are replaced and across revisions, and no other exam ever gets it. A copy of an exam gets a new code.
- The exam page shows the code large, for a projector, and the student link `<GOEXAM_PUBLIC_URL>/?code=XXXXXX`. The exams list shows each exam's code.
- Students open the app and type the code and their name. Any case works, and spaces and a dash are ignored. With the link, the name is all they type.
- A code works only while its exam is open. Otherwise the student reads "No open exam has that code. Check it with your teacher.", the same sentence for a mistyped code, for a closed exam and for an exam whose close time has passed.
- A code of a scheduled exam that is not open yet shows the exam's title and when it opens, for example "This exam opens at 08:00." (with the date when that is not today), both through the student link and after pressing Start. The student starts once the open time has come.
- Several exams can be open at once; opening one never closes another. Closing an exam stops new starts only; attempts in progress continue.
- A student with an exam in progress is sent back to it. A resume code needs no exam code.
- Exams imported before this version get their code the first time the new version starts.

## Schedule

- The exam page has a Schedule section with an open time and a close time. Save schedule sets them; the exams list shows them under the exam's status.
- Either time may be empty. Without an open time the exam opens as soon as the schedule is saved; without a close time it stays open until the teacher presses Close.
- The close time must be after the open time and in the future. An open time in the past opens the exam at once.
- The exam is open exactly from the open time until the close time; at the close time itself it is closed.
- The close time stops new starts only. Attempts in progress keep their full length.
- Open and Close remove the schedule and leave the exam open or closed. Saving a schedule on an exam opened with the button hands it to the schedule.
- Times are typed and shown in `GOEXAM_TIME_ZONE`.

## Archive

- Archive, on the exam page, works for any exam the teacher can see: open or closed, with or without attempts. There is no confirmation; Unarchive is the undo.
- Archiving closes the exam and removes its schedule. Attempts in progress continue to their own deadline; new starts are refused with the same sentence as for a mistyped code.
- Archived exams leave the exams list. "Show archived" lists every exam, with archived ones marked `Archived`; "Hide archived" returns to the usual list. When every exam is archived, the list says "All exams are archived."
- The exam page of an archived exam shows `Archived` and the date it was archived, in `GOEXAM_TIME_ZONE`. Open and Save schedule are refused until the exam is unarchived.
- Results, the CSV, renaming and deleting attempts, History, Download and Copy keep working on an archived exam, and so do Edit questions and Replace while it has no attempts. A copy of an archived exam is not archived.
- Unarchive returns the exam closed and without a schedule; it does not bring back an earlier schedule.
- Archive is per exam, for everyone who can see it: an admin who archives a teacher's exam hides it from that teacher's list too.
- Rolling back to the previous version (before archive): archived exams show as closed there, and its Open and Save schedule on them answer an error page and change nothing. Unarchive with this version first.

## Editor

- New exam, on the exams page, writes an exam in the browser: its title, settings and first question. The new exam is closed and opens on its editor page.
- Edit questions, on the exam page, lists the questions with Edit, Move up, Move down and Remove, plus Add question and Settings (title, shuffle, result shown, monitor, length). A question form holds the question text, two to ten options and the correct one; Add option, Remove, Up and Down change the list without saving, and Enter in a text field saves.
- The editor works only for an exam without attempts. Once a student has started, the exam is frozen as before.
- A form opened before another save of the same exam (in another tab or by another teacher) is refused: "This exam changed since you opened this page. Nothing was saved." The typed text stays in the form; reopen the question to see the current version.
- A question form can add an image (PNG, JPEG, GIF or WebP, up to 5 MB). Saving adds a line such as `![](3f2a….png)` (a name made from the file's content) at the end of the question text; move that line to place the image elsewhere, also into an option. When the end of the question text would hide that line (for example raw HTML there), the save is refused and no file is kept. A refused save cannot keep the chosen file, so choose it again.
- The text is the exam file format: `**bold**`, `*italics*`, `` `code` ``, fenced code blocks and image lines work as in a file, and the same rules refuse a save (for example an empty question or option). Write `\-` or `\*` for a line starting with `-` or an option ending with `*`. Text is stored as a file would be, without trailing spaces. A question or option that begins with a code block is stored with an empty first line and shows that way when opened again. A first line that starts with three backticks always opens a code block, so inline code there takes one backtick (`` `x` ``, not ```` ```x``` ````).
- A save that changes nothing makes no revision.

## Revisions, download and copy

- Importing an exam makes its revision 1; each Replace and each editor save makes the next one. A revision records its number, the time, the teacher and a summary: `Imported`, `Replaced from file`, `Copied from <title> (<code>), revision <N>`, or for an editor save `Created in the editor`, `Question N edited` (with `; correct option changed` when the correct option's text changed), `Question N added`, `Question N removed`, `Question N moved to M` or `Settings changed: ` and the changed settings.
- History, on the exam page, lists the revisions newest first. Any revision opens read-only with its settings, questions, correct options and images.
- Once an exam has an attempt it is frozen: no Replace, no editor save and no new revision. To change it, copy it.
- Each attempt records the revision it started on; the attempt page shows it as a link to that revision. The results list and the CSV do not show it.
- Download gives the current revision as `exam-<CODE>.md`, or `exam-<CODE>.zip` when the exam has images, also for a frozen exam. The file is in a standard layout: all four settings lines, questions numbered from 1, the correct option marked ` *`; the words of questions and options stay as typed. Importing it gives an equal exam.
- Copy exam makes a new closed exam with a new code and the same title, owned by whoever pressed Copy, starting at revision 1. Attempts, schedule and the open state are not copied.
- Exams imported before this version get revision 1 `Imported` the first time the new version starts, dated at their import, and their existing attempts are put on it. Their text is rebuilt from the stored pages, so a special character may come back with a backslash (for example `2\*3`), which shows the same; their images are named by content hash in a download. An exam whose text cannot be rebuilt exactly (it uses a list, link, quote, heading or raw HTML, bold-and-italic text written as `**_x_**` or `__*x*__`, or a paragraph ending in a no-break space) keeps working but has no history, download or copy; the exam page says so and the server log names the exam and the question at every start.
- Rolling back to the previous version (before the editor): edited exams keep working there like replaced ones (student pages, results, History, Download and Copy); it has no editor, so New exam and Edit questions are gone and their pages answer 404.
- Rolling back to the version before revisions: everything keeps working except Replace on an exam that has a revision, which is refused and changes nothing.

## Run with a container

The image is published as `ghcr.io/fiatcode-gh/go-exam`. It contains a single static binary, runs as uid/gid 65532 (non-root) and keeps its data in `/data`.

```sh
podman build -t localhost/go-exam:dev -f Containerfile .
podman volume create go-exam-data
podman run -d --name go-exam -p 8080:8080 \
  --env-file .env \
  -v go-exam-data:/data \
  ghcr.io/fiatcode-gh/go-exam:latest   # or localhost/go-exam:dev
```

Open `http://localhost:8080/` for students and `http://localhost:8080/admin` for the teacher.

## Behind a reverse proxy

- The app speaks plain HTTP; let the proxy terminate TLS and forward to port 8080.
- Cookies are always `Secure`, so students and the teacher must reach the app over HTTPS (browsers also accept `http://localhost` for local use).
- Set `GOEXAM_TRUSTED_PROXIES` to the proxy's network (for example `172.18.0.0/16`) so the request log shows real client addresses instead of the proxy's.
- The server needs outbound HTTPS to Google to sign teachers in. Students are not affected when Google cannot be reached.

## Browser support

Tested in Chromium on desktop and in Chrome on a real Android phone. Safari (iPhone, iPad, Mac) is not tested.

## Health check

`GET /healthz` returns `200` with body `ok` when the server and database are available, and `503` otherwise.

## Data and backup

Everything lives under the data directory:

- `go-exam.db` — the SQLite database (WAL mode, so `go-exam.db-wal` and `go-exam.db-shm` may sit next to it);
- `media/` — images imported with exams, kept for every revision: Replace no longer removes the old images, so this folder grows with each Replace.

To back up while the app is running, take an online copy of the database and copy the media directory:

```sh
sqlite3 "$db" ".backup '$dest'"
cp -a "$data/media" "$backup/"
```

## Exam file format

Upload either one text file (`.md` or `.txt`) or a `.zip` containing the text file plus the images it references. A complete example:

````markdown
# Chapter 3 quiz
shuffle: questions, options
show_result: review

1. What is the **area** of this triangle?
![A right triangle with legs 3 and 4](triangle.png)
- 6 *
- 7
- 12
- 14

2. What does this program print?
```
fmt.Println(2 + 3)
```
- `5` *
- `23`
- `2 + 3`
````

Rules:

- The first line is the title: `# Title`.
- Optional settings follow the title: `shuffle: none | questions | questions, options` (default `questions`), `show_result: none | score | review` (default `score`), `monitor: on | off` (default `on`) and `duration: <minutes>` (the time limit, a whole number from 5 to 240, default `60`). Like the questions, the length is fixed once an attempt exists.
- With `monitor: on`, the exam page blocks copy, cut, paste and the right-click menu, and reports to the teacher when the student leaves the page for more than 3 seconds, tries one of those blocked actions or resizes the window. The student sees "Your teacher has been notified." after each event, and the page asks the browser to keep the screen on. The teacher sees the count in the results list (Flags) and CSV (`flags`), and each event on the attempt page. The monitor cannot see other apps, other devices, screenshots or browser extensions; it discourages cheating, it does not prevent it. Events never change answers, time or score.
- Questions are numbered `1.`, `2.`, … at the start of a line.
- Options start with `- `. Each question needs at least two.
- Mark exactly one option per question as correct with a trailing `*`.
- Text supports bold (`**bold**`), italics (`*italics*`), inline `` `code` ``, line breaks, fenced code blocks (```` ``` ````) and images (`![description](file.png)`).
- To use images, upload a `.zip` containing the text file and the image files (PNG, JPEG, GIF or WebP). File names inside the zip must be valid UTF-8, which every current zip tool writes.
- Uploads are limited to 10 MB and must finish within 60 seconds, which needs an upload speed of about 1.4 Mbit/s for a 10 MB file.

## Development

```sh
gofmt -l .                                              # must print nothing
go vet ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...
go test ./...
go test -race ./...
podman build -t localhost/go-exam:dev -f Containerfile .

# Run with your own settings from .env (never printed):
( set -a; . ./.env; set +a; exec go run ./cmd/go-exam )

# Or run against the fake OpenID provider (tests, load test, browser scripts).
# The app must be reached as localhost and the provider as 127.0.0.1.
go run ./cmd/oidcfake                                   # serves on 127.0.0.1:9096
GOEXAM_GOOGLE_CLIENT_ID=dev-client GOEXAM_GOOGLE_CLIENT_SECRET=dev-secret \
GOEXAM_ADMIN_EMAILS=admin@example.test GOEXAM_PUBLIC_URL=http://localhost:8080 \
GOEXAM_OIDC_ISSUER=http://127.0.0.1:9096 GOEXAM_DATA_DIR=./data go run ./cmd/go-exam

go run ./cmd/loadtest -base-url http://localhost:8080 -teacher-email admin@example.test
# Each browser script expects a fresh data directory:
uv run e2e/student_ui.py --base-url http://localhost:8080 --admin-email admin@example.test --oidc-url http://127.0.0.1:9096 --out e2e/out/student-ui
uv run e2e/exam_flow.py  --base-url http://localhost:8080 --admin-email admin@example.test --oidc-url http://127.0.0.1:9096 --out e2e/out/exam-flow
uv run e2e/monitor.py    --base-url http://localhost:8080 --admin-email admin@example.test --oidc-url http://127.0.0.1:9096 --out e2e/out/monitor
uv run e2e/teachers.py   --base-url http://localhost:8080 --admin-email admin@example.test --oidc-url http://127.0.0.1:9096 --out e2e/out/teachers
uv run e2e/exam_codes.py --base-url http://localhost:8080 --admin-email admin@example.test --oidc-url http://127.0.0.1:9096 --out e2e/out/exam-codes
uv run e2e/revisions.py  --base-url http://localhost:8080 --admin-email admin@example.test --oidc-url http://127.0.0.1:9096 --out e2e/out/revisions
uv run e2e/editor.py     --base-url http://localhost:8080 --admin-email admin@example.test --oidc-url http://127.0.0.1:9096 --out e2e/out/editor
# Rollback rehearsal: start the previous release's binary on a copy of the data directory editor.py left, then:
uv run e2e/editor.py --rollback --base-url http://localhost:8080 --admin-email admin@example.test --oidc-url http://127.0.0.1:9096 --out e2e/out/editor-rollback
uv run e2e/archive.py    --base-url http://localhost:8080 --admin-email admin@example.test --oidc-url http://127.0.0.1:9096 --out e2e/out/archive
# Rollback rehearsal: start the previous release's binary (5716eb8) on a copy of the data directory archive.py left, then:
uv run e2e/archive.py --rollback --base-url http://localhost:8080 --admin-email admin@example.test --oidc-url http://127.0.0.1:9096 --out e2e/out/archive-rollback
# The server must run with GOEXAM_TIME_ZONE=Asia/Jakarta; the run waits about two minutes on the real clock:
uv run e2e/scheduled_exams.py --base-url http://localhost:8080 --admin-email admin@example.test --oidc-url http://127.0.0.1:9096 --time-zone Asia/Jakarta --out e2e/out/scheduled-exams

# Check that every exam in a database copy can be given its revision 1 (read-only; use a copy taken with sqlite3 ".backup"):
go run ./cmd/rebuildcheck -db /path/to/copy.db
```

The fake provider signs in any identity and is not part of the container image.
