# go-exam: decisions

A self-hosted web app for classroom multiple-choice exams. Students type only their name; teachers sign in with Google, import exams from text files and review the results of their own exams. Built 2026-11-13 to 2026-11-15; teacher accounts added 2026-11-16.

## Scope

- In: Google sign-in for teachers; a teacher list managed by admins; exam ownership (each teacher sees only their own exams, admins see all); one open exam at a time; Markdown-like exam files (or a zip with images); a cookie-bound attempt plus a teacher-issued resume code; a 60-minute wall-clock limit enforced by the server; shuffle and `show_result` set per exam; attempts list (sortable, filter by name), detail, teacher rename, CSV export, delete; a container image for the existing Traefik/Podman VPS.
- Out: student accounts, a Workspace-domain rule, password sign-in, moving an exam to another teacher, promoting admins in the app, an audit history of teacher changes, a web question editor, per-question statistics, math rendering, several open exams, other durations, anti-cheating beyond shuffle and the monitor below (no fullscreen, no force-submit, no live push), editing answers, deleting whole exams, a live dashboard. Deployment wiring (quadlet, Traefik route, backup and drill scripts, Gatus, Diun, DNS) belongs to a separate `fiatcode-infra` unit.

## Decisions and why

- **Go, server-rendered, one SQLite connection.** The VPS has 3.8 GiB and no swap. One `*sql.DB` with `MaxOpenConns(1)` and `_txlock=immediate` makes every write serial, so races (start vs replace, open vs open, resume swap) are closed by transactions instead of locks. Measured: 100 students, 4000 saves, p95 about 55 ms, about 20 MB of memory.
- **Invariants live in the schema too.** A partial unique index allows only one open exam, and triggers freeze questions and options once an attempt exists. A future code path that forgets the service check still cannot change an answer key after scoring started.
- **Nothing derived is stored.** The score is counted from answers, and the shuffle order is recomputed from a per-attempt seed. That leaves no stale copies to keep in sync.
- **Deadline:** 60 minutes from start plus a 3 s grace for network delay, invisible to the student. One sweep statement finalises overdue attempts; it runs at the start of every attempt transaction and every 5 s in the background, so abandoned attempts become `timed out` with no browser connected. Submit at or after the deadline records `timed out`.
- **Answer-key isolation by type.** Student view types have no correctness field. Tests compare full responses from two apps with different keys, byte for byte.
- **Resume codes** (8 characters, no ambiguous letters) always exist for unfinished attempts. Redeeming one swaps the cookie token and issues a new code in one transaction, so the old device loses access.
- **Images** go through one `os.Root`, under a random per-import key with content-hash names. Zip entry names never reach a filesystem call, and image types are checked by content sniffing (PNG, JPEG, GIF, WebP; no SVG).
- **Admin requests** get a fresh 15 s database budget after the upload body is read. Upload time is bounded by the 60 s `ReadTimeout` instead (about 1.4 Mbit/s for 10 MiB).
- **Countdown on a monotonic clock** (`performance.now()`), re-anchored on every server response, so moving the device clock cannot trigger an early submit.
- **Anti-cheat monitor** (added 2026-11-16, amends "anti-cheating beyond shuffle"): a per-exam `monitor: on | off` header, default on; migration 0002 adds `exam.monitor` (existing exams on) and an `attempt_event` table. The exam page blocks copy, cut, paste and the context menu and reports them, plus leaving the page for more than 3 s (counted once per absence) and resizes (width only on touch devices); the student sees "Your teacher has been notified." for 5 s after each event. The banner hangs under the sticky bar and overlays the page, so showing or hiding it moves no question; once the bar is stuck at the top of the window, jump targets and the navigator move below it while it shows. Events have their own queue in the page with a single backed-off retry, separate from answer saves, and carry the server's time and the question's file position: for copy, cut, paste and right-click the question that contains the pressed or selected element, otherwise (and always for leaving the page and resizing) the first question still visible below the bar. The monitor starts after the countdown, inside `try/catch`, so it cannot stop timing or saving; the banner lookups run inside it too, so a page without the banner loses only the monitor. The server stores events only for the cookie's in-progress attempt with the monitor on, at most 500 per attempt, and never reads them for score or timing. A screen wake lock, when the browser grants one, avoids false flags from a sleeping screen. It detects and discourages; it cannot see other apps, devices, screenshots or extensions, and a student can still disable JavaScript-side reporting.
- **Flags show the student's number** (2026-11-16, after the first real exam). With a shuffled exam the stored file position was not the number the student saw. The attempt page now shows "Question 3 on screen (file question 7)"; the screen number is recomputed from the attempt's seed and the exam's shuffle mode through the same layout as the student's page, so nothing derived is stored and no migration is needed. An unshuffled exam shows one number.
- **Teacher pages show clock times without seconds** (2026-11-16). The nine-column attempts table did not fit the 1100 px page column and "Delete" was cut off. The table shows `HH:MM` with the full date and time in a tooltip; one date line sits above it when every shown attempt started on the same date in the configured zone, otherwise (and for any time on another date) the cell carries a short date. The attempt page shows date and `HH:MM`. The "Time left" countdown keeps `MM:SS`. The CSV keeps RFC 3339 with seconds.
- **Name filter** (2026-11-16). A plain GET field `name`, a case-insensitive substring match applied in the handler after the list is loaded; sort links keep it. The CSV always holds every attempt, so an export never silently misses students.
- **Teacher rename** (2026-11-16). `POST /admin/exams/{id}/attempts/{aid}/rename` behind the admin session and CSRF token, for an attempt in any status, with the same name rule as a student's start. The name is overwritten with no history and nothing else in the attempt changes; students have no rename route, so a typo is fixed by the teacher, not by a second attempt.
- **Google sign-in** (2026-11-16, replaces the shared admin password). Authorization-code flow with `state`, `nonce` and PKCE `S256`, kept in one 10-minute `SameSite=Lax` cookie on `/admin/auth` that every callback expires. `go-oidc` verifies the ID token's signature against the provider's keys, issuer, audience and expiry; the app adds exactly one audience, the nonce and `email_verified`. Discovery is lazy and failures are not cached, so a server that cannot reach Google at boot still serves students. Logs carry fixed reason words only, never an email, code, state or token.
- **Allow-list and roles** (2026-11-16). A teacher is identified by the normalized email (trimmed, lower case); signing in needs a verified email and a `teacher` row with `allowed = 1`. Removal is a flag, because exams keep pointing at the row. Admins are exactly `GOEXAM_ADMIN_EMAILS`, put on the list at startup; the role is computed on every request and never stored, so the variable alone decides it.
- **Teacher sessions** (2026-11-16). One `admin_session` row per sign-in, owned by a teacher, for 12 hours. Sessions survive restarts and end by sign-out or by removal (rows deleted, and the session query joins on `allowed`). The old ownerless sessions were cleared at every start anyway, so migration 0003 drops them.
- **Exam ownership in one place per layer** (2026-11-16). `exam.Service` methods take an `Actor` and apply one condition (admin, or `owner_id` is the actor); anything else is `ErrNotFound`, so another teacher's exam answers 404 on every route. Results handlers resolve the exam through that check before touching attempts, which stay scoped by exam id. Migration 0004 adds a nullable `exam.owner_id` (SQLite refuses a `REFERENCES` column with a non-NULL default) plus triggers that refuse a NULL owner on insert or update; at every start the ownerless exams go to the first `GOEXAM_ADMIN_EMAILS` entry, which touches only NULL owners and so moves nothing later.
- **One open exam, for now** (2026-11-16). The unique index stays until exam codes (unit B). Opening an exam implicitly closes only the acting teacher's own open exam; when another teacher's exam is open, teachers and admins alike get a 409 with a sentence that names no exam, and an admin closes the other exam explicitly first. No running exam is closed by someone else's click.
- **Fake OpenID provider** (2026-11-16). `internal/oidcfake` and `cmd/oidcfake` give tests, the load test and the browser scripts a real provider (discovery, keys, PKCE and client-secret checks). It is no bypass: the app has no test switch, the only way to reach the fake is `GOEXAM_OIDC_ISSUER`, which must be `https` or loopback, `cmd/go-exam` does not link it (a test checks), the image builds only `./cmd/go-exam`, and `cmd/oidcfake` listens on loopback only.

## Rejected alternatives

- A stored score column or stored shuffle order: both create stale-copy bugs.
- Resume by name: anyone could open a classmate's attempt.
- A test-only switch for a shorter exam duration or insecure cookies: tests inject time instead.
- A backdoor password next to Google sign-in: a second way in that every attacker would try first. Recovery is an environment change plus a restart.
- Storing the sign-in state in a table: anyone could write rows without signing in.
- Provider discovery at startup: an unreachable provider would stop the server, and with it the students.
- Relaxing the session cookie to `SameSite=Lax` so the callback could redirect: it would weaken CSRF protection for every teacher request to fix one landing page.

## Traps

- With one connection, calling `s.db` or another service method inside an open transaction deadlocks. Scan and close rows before the next statement.
- Go's cookie jar will not send `Secure` cookies over plain HTTP. Test and load clients set the `Cookie` header by hand. Browsers do send them on `http://localhost`.
- Chromium logs non-2xx page loads as console errors that page code cannot suppress. The e2e scripts exempt exactly the 422 of a reused resume code.
- The session cookie is `SameSite=Strict`, and a browser withholds it on a redirect chain that started on another site. The sign-in callback therefore answers 200 with a meta refresh and never redirects into `/admin`. The browser scripts run the fake provider on `127.0.0.1` and the app on `localhost`, so the callback crosses sites as it does with Google, and they refuse equal hostnames.
- The sign-in button is a link to `/admin/auth/start`, not a form: under `form-action 'self'` Chromium blocks a form submission that redirects to the provider.
- An OpenID request (discovery, keys, token exchange) must never run inside an open database transaction: with one connection, a slow provider would stall every request.

## Open questions and known limits

- Option ids follow file order, so the page source reveals the original option order under `shuffle: questions, options`.
- `/start` and `/resume` are not throttled. Guessing a code at 100 active attempts has odds of about 1.5e-10 per try.
- On some devices the countdown may stall while asleep and offline. The server still rejects late answers.
- The interface text is English.
- Monitor flags are evidence for a conversation, not proof: a notification shade, an incoming call, a rotated phone or a long-press on a phone (logged as "Right-clicked") also counts. On touch devices only width changes count as a resize, so sliding address bars raise no flags. Leaving the page and resizing name the first visible question, which can still be the previous one.
- Clock times on teacher pages have no seconds, so several flags in one minute show the same time; they are still listed in order.
- At the very top of the exam page, before the bar is stuck, the monitor banner briefly covers the first question's heading and the top of the navigator for its 5 s.
- A student name that is one unbroken word wider than about 220 px still makes the attempts table scroll sideways.
- Browser proof covers desktop Chromium and, for the monitor, Chrome on a real Android phone (2026-11-16). Safari is not tested; there is no Apple device to test on.
- On Android a long-press is flagged and its menu blocked, but Chrome still selects the word and offers a "Tap to see search results" sheet.
- An email taken out of `GOEXAM_ADMIN_EMAILS` stays an ordinary teacher until an admin removes it on the Teachers page.
- Removed teachers are not shown on the Teachers page; their exams stay on the admin's exams list with their email as owner.
- The allow-list is keyed by email, not by Google's account id, so a re-issued address would inherit access.
- One exam is open at a time across all teachers until exam codes (unit B). An admin must close a removed teacher's open exam.
- Sign-in is proven in Chromium only; other browsers are covered by the live check.
