# go-exam: decisions

A self-hosted web app for classroom multiple-choice exams. Students type only their name; the teacher imports exams from text files and reviews results. Built 2026-11-13 to 2026-11-15.

## Scope

- In: a single admin password; one open exam at a time; Markdown-like exam files (or a zip with images); a cookie-bound attempt plus a teacher-issued resume code; a 60-minute wall-clock limit enforced by the server; shuffle and `show_result` set per exam; attempts list (sortable, filter by name), detail, teacher rename, CSV export, delete; a container image for the existing Traefik/Podman VPS.
- Out: teacher accounts (Authelia in front of `/admin` stays a later Traefik-only option), a web question editor, per-question statistics, math rendering, several open exams, other durations, anti-cheating beyond shuffle and the monitor below (no fullscreen, no force-submit, no live push), editing answers, deleting whole exams, a live dashboard. Deployment wiring (quadlet, Traefik route, backup and drill scripts, Gatus, Diun, DNS) belongs to a separate `fiatcode-infra` unit.

## Decisions and why

- **Go, server-rendered, one SQLite connection.** The VPS has 3.8 GiB and no swap. One `*sql.DB` with `MaxOpenConns(1)` and `_txlock=immediate` makes every write serial, so races (start vs replace, open vs open, resume swap) are closed by transactions instead of locks. Measured: 100 students, 4000 saves, p95 about 55 ms, about 20 MB of memory.
- **Invariants live in the schema too.** A partial unique index allows only one open exam, and triggers freeze questions and options once an attempt exists. A future code path that forgets the service check still cannot change an answer key after scoring started.
- **Nothing derived is stored.** The score is counted from answers, and the shuffle order is recomputed from a per-attempt seed. That leaves no stale copies to keep in sync.
- **Deadline:** 60 minutes from start plus a 3 s grace for network delay, invisible to the student. One sweep statement finalises overdue attempts; it runs at the start of every attempt transaction and every 5 s in the background, so abandoned attempts become `timed out` with no browser connected. Submit at or after the deadline records `timed out`.
- **Answer-key isolation by type.** Student view types have no correctness field. Tests compare full responses from two apps with different keys, byte for byte.
- **Resume codes** (8 characters, no ambiguous letters) always exist for unfinished attempts. Redeeming one swaps the cookie token and issues a new code in one transaction, so the old device loses access.
- **Images** go through one `os.Root`, under a random per-import key with content-hash names. Zip entry names never reach a filesystem call, and image types are checked by content sniffing (PNG, JPEG, GIF, WebP; no SVG).
- **Admin requests** get a fresh 15 s database budget after the upload body is read. Upload time is bounded by the 60 s `ReadTimeout` instead (about 1.4 Mbit/s for 10 MiB).
- **Admin sessions are cleared at startup**, so changing the password and restarting signs everyone out.
- **The login throttle** counts each attempt before checking the password, under one lock, so concurrent guesses cannot exceed the free failures.
- **Countdown on a monotonic clock** (`performance.now()`), re-anchored on every server response, so moving the device clock cannot trigger an early submit.
- **Anti-cheat monitor** (added 2026-11-16, amends "anti-cheating beyond shuffle"): a per-exam `monitor: on | off` header, default on; migration 0002 adds `exam.monitor` (existing exams on) and an `attempt_event` table. The exam page blocks copy, cut, paste and the context menu and reports them, plus leaving the page for more than 3 s (counted once per absence) and resizes (width only on touch devices); the student sees "Your teacher has been notified." for 5 s after each event. The banner hangs under the sticky bar and overlays the page, so showing or hiding it moves no question; once the bar is stuck at the top of the window, jump targets and the navigator move below it while it shows. Events have their own queue in the page with a single backed-off retry, separate from answer saves, and carry the server's time and the question's file position: for copy, cut, paste and right-click the question that contains the pressed or selected element, otherwise (and always for leaving the page and resizing) the first question still visible below the bar. The monitor starts after the countdown, inside `try/catch`, so it cannot stop timing or saving; the banner lookups run inside it too, so a page without the banner loses only the monitor. The server stores events only for the cookie's in-progress attempt with the monitor on, at most 500 per attempt, and never reads them for score or timing. A screen wake lock, when the browser grants one, avoids false flags from a sleeping screen. It detects and discourages; it cannot see other apps, devices, screenshots or extensions, and a student can still disable JavaScript-side reporting.
- **Flags show the student's number** (2026-11-16, after the first real exam). With a shuffled exam the stored file position was not the number the student saw. The attempt page now shows "Question 3 on screen (file question 7)"; the screen number is recomputed from the attempt's seed and the exam's shuffle mode through the same layout as the student's page, so nothing derived is stored and no migration is needed. An unshuffled exam shows one number.
- **Teacher pages show clock times without seconds** (2026-11-16). The nine-column attempts table did not fit the 1100 px page column and "Delete" was cut off. The table shows `HH:MM` with the full date and time in a tooltip; one date line sits above it when every shown attempt started on the same date in the configured zone, otherwise (and for any time on another date) the cell carries a short date. The attempt page shows date and `HH:MM`. The "Time left" countdown keeps `MM:SS`. The CSV keeps RFC 3339 with seconds.
- **Name filter** (2026-11-16). A plain GET field `name`, a case-insensitive substring match applied in the handler after the list is loaded; sort links keep it. The CSV always holds every attempt, so an export never silently misses students.
- **Teacher rename** (2026-11-16). `POST /admin/exams/{id}/attempts/{aid}/rename` behind the admin session and CSRF token, for an attempt in any status, with the same name rule as a student's start. The name is overwritten with no history and nothing else in the attempt changes; students have no rename route, so a typo is fixed by the teacher, not by a second attempt.

## Rejected alternatives

- A stored score column or stored shuffle order: both create stale-copy bugs.
- Resume by name: anyone could open a classmate's attempt.
- A test-only switch for a shorter exam duration or insecure cookies: tests inject time instead.

## Traps

- With one connection, calling `s.db` or another service method inside an open transaction deadlocks. Scan and close rows before the next statement.
- Go's cookie jar will not send `Secure` cookies over plain HTTP. Test and load clients set the `Cookie` header by hand. Browsers do send them on `http://localhost`.
- Chromium logs non-2xx page loads (401 wrong password, 422 reused resume code) as console errors that page code cannot suppress. The e2e scripts exempt exactly those.
- Behind Traefik, `GOEXAM_TRUSTED_PROXIES` must be set. Without it every client shares one throttle key, and anyone can lock teacher login for up to 15 minutes.

## Open questions and known limits

- Option ids follow file order, so the page source reveals the original option order under `shuffle: questions, options`.
- `/start` and `/resume` are not throttled. Guessing a code at 100 active attempts has odds of about 1.5e-10 per try.
- On some devices the countdown may stall while asleep and offline. The server still rejects late answers.
- The interface text is English.
- Monitor flags are evidence for a conversation, not proof: a notification shade, an incoming call, a rotated phone or a long-press on a phone (logged as "Right-clicked") also counts. On touch devices only width changes count as a resize, so sliding address bars raise no flags. Leaving the page and resizing name the first visible question, which can still be the previous one.
- Clock times on teacher pages have no seconds, so several flags in one minute show the same time; they are still listed in order.
- At the very top of the exam page, before the bar is stuck, the monitor banner briefly covers the first question's heading and the top of the navigator for its 5 s.
- A student name that is one unbroken word wider than about 220 px still makes the attempts table scroll sideways.
- CI is unproven until the first push. Browser proof covers Chromium only; the banner overlay on iOS Safari and Android is checked on real phones after deploy.
