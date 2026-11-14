# go-exam

go-exam is a small, self-hosted web app for classroom multiple-choice exams. Students type only their name and take the currently open exam within a one-hour limit; the teacher imports exams from text files, opens and closes them, and reviews the results.

It is not a learning platform: there are no student or teacher accounts (the teacher signs in with one shared admin password), and only one exam is open at a time.

## Configuration

All configuration comes from environment variables.

| Variable | Default | Meaning |
| --- | --- | --- |
| `GOEXAM_ADMIN_PASSWORD` | none (required) | Teacher password; must be at least 12 characters. |
| `GOEXAM_DATA_DIR` | `./data` (image: `/data`) | Directory holding the database and uploaded images. |
| `GOEXAM_LISTEN_ADDR` | `:8080` | Address the HTTP server listens on. |
| `GOEXAM_TRUSTED_PROXIES` | empty (trust nobody) | Comma-separated CIDRs or IPs whose `X-Forwarded-For` is trusted. |
| `GOEXAM_TIME_ZONE` | `UTC` | IANA time zone used to display times (time zone data is built in). |

The app refuses to start when `GOEXAM_ADMIN_PASSWORD` is missing or shorter than 12 characters.

## Run with a container

The image is published as `ghcr.io/fiatcode-gh/go-exam`. It contains a single static binary, runs as uid/gid 65532 (non-root) and keeps its data in `/data`.

```sh
podman build -t localhost/go-exam:dev -f Containerfile .
podman volume create go-exam-data
podman run -d --name go-exam -p 8080:8080 \
  -e GOEXAM_ADMIN_PASSWORD='change-me-please' \
  -v go-exam-data:/data \
  ghcr.io/fiatcode-gh/go-exam:latest   # or localhost/go-exam:dev
```

Open `http://localhost:8080/` for students and `http://localhost:8080/admin` for the teacher.

## Behind a reverse proxy

- The app speaks plain HTTP; let the proxy terminate TLS and forward to port 8080.
- Cookies are always `Secure`, so students and the teacher must reach the app over HTTPS (browsers also accept `http://localhost` for local use).
- Set `GOEXAM_TRUSTED_PROXIES` to the proxy's network (for example `172.18.0.0/16`) so login throttling sees real client addresses instead of the proxy's.

## Health check

`GET /healthz` returns `200` with body `ok` when the server and database are available, and `503` otherwise.

## Data and backup

Everything lives under the data directory:

- `go-exam.db` — the SQLite database (WAL mode, so `go-exam.db-wal` and `go-exam.db-shm` may sit next to it);
- `media/` — images imported with exams.

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
- Optional settings follow the title: `shuffle: none | questions | questions, options` (default `questions`) and `show_result: none | score | review` (default `score`).
- Questions are numbered `1.`, `2.`, … at the start of a line.
- Options start with `- `. Each question needs at least two.
- Mark exactly one option per question as correct with a trailing `*`.
- Text supports bold (`**bold**`), italics (`*italics*`), inline `` `code` ``, line breaks, fenced code blocks (```` ``` ````) and images (`![description](file.png)`).
- To use images, upload a `.zip` containing the text file and the image files (PNG, JPEG, GIF or WebP).
- Uploads are limited to 10 MB.

## Development

```sh
gofmt -l .                                              # must print nothing
go vet ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...
go test ./...
go test -race ./...
podman build -t localhost/go-exam:dev -f Containerfile .
GOEXAM_ADMIN_PASSWORD=dev-password-123 GOEXAM_DATA_DIR=./data go run ./cmd/go-exam   # serves on :8080
go run ./cmd/loadtest                                   # load test against a running instance
uv run e2e/exam_flow.py                                 # browser end-to-end exam flow
```
