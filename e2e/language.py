# /// script
# requires-python = ">=3.12"
# dependencies = ["playwright==1.63.0"]
# ///
"""Browser check for the language switch in real Chromium: the first visit
follows the browser's language, the switch works without scripts on every
kind of page and keeps the address, its cookie wins over the header, a
refused form returns to a page, and on the exam page (monitor on) the switch
saves every answer before it reloads, raises no flag and leaves the monitor
working; a save that cannot finish holds the switch back. Teacher pages are
checked through html[lang], addresses and attributes only. Needs a fresh
instance.

uv run e2e/language.py --base-url URL --admin-email EMAIL --oidc-url URL --out DIR
"""

import argparse
import re
import sys
import tempfile
import time
import zipfile
from contextlib import contextmanager
from datetime import datetime, timezone
from pathlib import Path

from playwright.sync_api import Page, expect, sync_playwright

from common import (
    add_sign_in_args,
    exam_code,
    foreign_requests,
    import_and_open,
    import_exam,
    require_cross_site,
    start,
    teacher_login,
    watch,
)

DESKTOP = {"viewport": {"width": 1280, "height": 800}}
PHONE = {"viewport": {"width": 360, "height": 640}}
COOKIE = "answerbox_lang"
ENGLISH_BANNER = "Your teacher has been notified."

EXAM_MD = """# Language check
shuffle: none
monitor: on
show_result: review

1. Which city is the capital of Indonesia?
- Jakarta *
- Bandung
- Surabaya

2. What is 15 + 27?
= 42
"""


class StepFailed(Exception):
    pass


@contextmanager
def step(name: str):
    print(f"step: {name}", flush=True)
    try:
        yield
    except Exception as err:  # every failure names its step
        raise StepFailed(f"FAIL [{name}]: {err}") from err


def write_exam_zip(dir: Path) -> Path:
    path = dir / "exam.zip"
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as z:
        z.writestr("exam.md", EXAM_MD)
    return path


def html_lang(page: Page) -> str:
    return page.locator("html").get_attribute("lang")


def open_switch(page: Page) -> None:
    """Open the switch's menu unless it is open already."""
    switch = page.locator("#lang-switch")
    if switch.get_attribute("open") is None:
        switch.locator("summary").click()


def lang_button(page: Page, value: str):
    return page.locator(f"#lang-switch button[name=lang][value={value}]")


def press(page: Page, value: str) -> None:
    """Open the switch and press the button for value ("id" or "en")."""
    open_switch(page)
    lang_button(page, value).click()


def has_switch_once(page: Page) -> None:
    """Check 5: one visible switch outside main, with two buttons."""
    expect(page.locator("#lang-switch")).to_have_count(1)
    expect(page.locator("#lang-switch")).to_be_visible()
    expect(page.locator("main #lang-switch")).to_have_count(0)
    expect(page.locator("#lang-switch button[name=lang]")).to_have_count(2)


def switch_keeps_address(page: Page, url: str, status: int = 200) -> None:
    """Check 2 on one page: open url (answered with status), press Indonesia; same address, Indonesian, pressed button current."""
    if (got := page.goto(url).status) != status:
        raise AssertionError(f"{url} answered {got}, want {status}")
    has_switch_once(page)
    before = page.url
    press(page, "id")
    expect(page).to_have_url(before)
    expect(page.locator("html")).to_have_attribute("lang", "id")
    expect(lang_button(page, "id")).to_have_attribute("aria-current", "true")
    expect(lang_button(page, "en")).not_to_have_attribute("aria-current", "true")


def cookie(context) -> dict | None:
    return next((c for c in context.cookies() if c["name"] == COOKIE), None)


def question(page: Page, n: int):
    return page.locator(f"section.question#q{n}")


def picked(page: Page, n: int) -> str | None:
    radio = question(page, n).locator("input[type=radio]:checked")
    return radio.get_attribute("value") if radio.count() else None


def results_row(teacher: Page, base_url: str, exam_id: int, name: str):
    teacher.goto(f"{base_url}/admin/exams/{exam_id}/attempts")
    return teacher.locator("#attempts tbody tr", has_text=name)


def flags(teacher: Page, base_url: str, exam_id: int, name: str) -> str:
    return results_row(teacher, base_url, exam_id, name).locator("td.flags").get_attribute("data-flags")


def wait_for_flags(teacher: Page, base_url: str, exam_id: int, name: str, want: str, seconds: float) -> None:
    deadline = time.monotonic() + seconds
    while (got := flags(teacher, base_url, exam_id, name)) != want:
        if time.monotonic() > deadline:
            raise AssertionError(f"results row has data-flags={got}, want {want}")
        teacher.wait_for_timeout(250)


def attempt_href(teacher: Page, base_url: str, exam_id: int, name: str) -> str:
    row = results_row(teacher, base_url, exam_id, name)
    return row.locator(f"a[href^='/admin/exams/{exam_id}/attempts/']").first.get_attribute("href")


def event_rows(teacher: Page, base_url: str, exam_id: int, name: str) -> int:
    teacher.goto(base_url + attempt_href(teacher, base_url, exam_id, name))
    expect(teacher.locator("#flags")).to_be_visible()
    return teacher.locator("#events tr.event").count()


def timer_runs(page: Page) -> None:
    first = page.locator("#timer").inner_text()
    page.wait_for_function(
        "first => document.getElementById('timer').textContent !== first", arg=first, timeout=2000
    )


def copy(page: Page) -> None:
    """Select question 1's text and press Ctrl+C (dispatching the event if Chromium fires none)."""
    body = question(page, 1).locator(".question-body")
    body.click(click_count=3)
    page.evaluate("window.__copied = false; document.addEventListener('copy', () => { window.__copied = true; }, {once: true})")
    page.keyboard.press("Control+C")
    if not page.evaluate("window.__copied"):
        print("  note: Ctrl+C fired no copy event; dispatching one", flush=True)
        body.dispatch_event("copy")


def foot_screenshot(page: Page, path: Path) -> None:
    page.evaluate("window.scrollTo(0, document.documentElement.scrollHeight)")
    page.screenshot(path=path, animations="disabled")


def run(base_url: str, email: str, oidc_url: str, out: Path) -> None:
    out.mkdir(parents=True, exist_ok=True)
    logs = []
    with sync_playwright() as p, tempfile.TemporaryDirectory() as tmp:
        browser = p.chromium.launch(headless=True)

        def new_context(**kwargs):
            context = browser.new_context(**kwargs)
            logs.append(watch(context, base_url))
            return context

        teacher = new_context(locale="en-US", **DESKTOP).new_page()
        with step("0 setup: the teacher imports and opens the exam of check 6, imports a draft for the editor; Budi starts without scripts"):
            teacher_login(teacher, base_url, email)
            exam_id = import_and_open(teacher, write_exam_zip(Path(tmp)))
            code = exam_code(teacher, exam_id)
            teacher.goto(f"{base_url}/admin/exams")
            draft_id = import_exam(teacher, write_exam_zip(Path(tmp)))  # no attempts: its editor is not frozen
            budi = new_context(java_script_enabled=False, locale="en-US", **DESKTOP).new_page()
            start(budi, base_url, code, "Budi Santoso")
            budi_attempt = attempt_href(teacher, base_url, exam_id, "Budi Santoso")

        with step("1 first visit follows Accept-Language and sets no cookie"):
            for locale, want in [("id-ID", "id"), ("en-US", "en"), ("fr-FR", "en")]:
                context = new_context(locale=locale, **DESKTOP)
                page = context.new_page()
                page.goto(base_url + "/")
                if (got := html_lang(page)) != want:
                    raise AssertionError(f"locale {locale}: html[lang]={got}, want {want}")
                if cookie(context) is not None:
                    raise AssertionError(f"locale {locale}: {COOKIE} cookie set on the first visit")
                if locale == "en-US":
                    foot_screenshot(page, out / "landing-foot-en.png")
                context.close()

        nojs_context = new_context(java_script_enabled=False, locale="en-US", **DESKTOP)
        nojs = nojs_context.new_page()
        with step("2a without scripts on /: Indonesia keeps the address and the page turns Indonesian (with check 5)"):
            switch_keeps_address(nojs, base_url + "/")
            foot_screenshot(nojs, out / "landing-foot-id.png")

        with step("3a the cookie: answerbox_lang=id, path /, HttpOnly, Secure, SameSite=Lax, about a year"):
            c = cookie(nojs_context)
            if c is None:
                raise AssertionError(f"no {COOKIE} cookie after the press")
            want = {"value": "id", "path": "/", "httpOnly": True, "secure": True, "sameSite": "Lax"}
            got = {k: c[k] for k in want}
            if got != want:
                raise AssertionError(f"cookie {got}, want {want}")
            days = (c["expires"] - datetime.now(timezone.utc).timestamp()) / 86400
            if not 364 <= days <= 366:
                raise AssertionError(f"cookie expires in {days:.2f} days, want 364 to 366")

        with step("2b without scripts on every other kind of page: same address, Indonesian, Indonesia current (with check 5)"):
            for url, status in [
                (f"{base_url}/?code={code}", 200),
                (f"{base_url}/admin/login", 200),
                (f"{base_url}/no-such-page", 404),
            ]:
                switch_keeps_address(nojs, url, status)
            teacher_login(nojs, base_url, email)  # the provider's form works without scripts
            for path in [
                "/admin/exams",
                f"/admin/exams/{exam_id}",
                f"/admin/exams/{exam_id}/attempts?sort=name&dir=desc",
                budi_attempt,
                f"/admin/exams/{exam_id}/revisions",
                f"/admin/exams/{draft_id}/edit",
                f"/admin/exams/{draft_id}/questions/1",
                "/admin/teachers",
            ]:
                switch_keeps_address(nojs, base_url + path)

        with step("2c one press of English on /admin/exams flips back"):
            nojs.goto(f"{base_url}/admin/exams")
            press(nojs, "en")
            expect(nojs).to_have_url(f"{base_url}/admin/exams")
            expect(nojs.locator("html")).to_have_attribute("lang", "en")
            expect(lang_button(nojs, "en")).to_have_attribute("aria-current", "true")
            if (c := cookie(nojs_context)) is None or c["value"] != "en":
                raise AssertionError(f"cookie after English: {c}")

        with step("3b the cookie wins over the header: id-ID presses English, the next three pages are English"):
            idctx = new_context(locale="id-ID", **DESKTOP)
            idp = idctx.new_page()
            idp.goto(base_url + "/")
            expect(idp.locator("html")).to_have_attribute("lang", "id")
            press(idp, "en")
            expect(idp.locator("html")).to_have_attribute("lang", "en")
            for path in ["/", "/admin/login", "/no-such-page"]:
                idp.goto(base_url + path)
                if (got := html_lang(idp)) != "en":
                    raise AssertionError(f"{path}: html[lang]={got}, want en")
            idctx.close()

        with step("4 without scripts, a refused start shows the alert and the switch lands on / (a page, not a 404)"):
            nojs.goto(base_url + "/")
            form = nojs.locator("#start-form")
            form.locator("input[name=code]").fill("ZZZZZZ")
            form.locator("input[name=name]").fill("Dewi Lestari")
            form.locator("button[type=submit]").click()
            expect(nojs.locator("#start-alert")).to_be_visible()
            has_switch_once(nojs)
            open_switch(nojs)
            with nojs.expect_navigation() as nav:
                lang_button(nojs, "id").click()
            if nav.value is None or nav.value.status != 200:
                raise AssertionError(f"the switch landed with status {nav.value and nav.value.status}, want 200")
            expect(nojs).to_have_url(base_url + "/")
            expect(nojs.locator("html")).to_have_attribute("lang", "id")
            expect(nojs.locator("#start-form")).to_be_visible()

        student_context = new_context(locale="en-US", **PHONE)
        s = student_context.new_page()
        dialogs = []

        def on_dialog(dialog):
            dialogs.append(dialog.type)
            dialog.accept()

        s.on("dialog", on_dialog)
        timeline = []
        s.on("request", lambda r: r.url.endswith("/lang") and timeline.append(("lang request", time.monotonic())))
        s.on(
            "response",
            lambda r: r.url.endswith("/exam/answer") and timeline.append(("answer response", time.monotonic())),
        )

        def hold(route):
            s.wait_for_timeout(1500)
            route.continue_()

        with step("6 a student (en-US, monitor on) starts"):
            start(s, base_url, code, "Ayu Lestari")
            expect(s.locator("main#exam")).to_have_attribute("data-monitor", "on")
            expect(s.locator("html")).to_have_attribute("lang", "en")
            english_saved = s.locator("#exam-text").get_attribute("data-all-saved")
            s.screenshot(path=out / "exam-before-switch.png", animations="disabled")

        with step("6a answers held 1500 ms: pick, type, press Indonesia at once"):
            s.route("**/exam/answer", hold)
            timeline.clear()
            question(s, 1).locator("label.option").nth(0).click()
            choice = picked(s, 1)
            question(s, 2).locator("input.answer-text").fill("42")
            pressed = time.monotonic()
            press(s, "id")
            expect(s.locator("html")).to_have_attribute("lang", "id", timeout=10000)
            s.unroute("**/exam/answer")

        with step("6b /lang is requested only after both answer responses arrived"):
            order = [(kind, round(t - pressed, 3)) for kind, t in timeline]
            print(f"  timeline (s after the press): {order}", flush=True)
            kinds = [kind for kind, _ in timeline]
            if "lang request" not in kinds:
                raise AssertionError(f"no /lang request: {order}")
            before = kinds[: kinds.index("lang request")]
            if before.count("answer response") < 2:
                raise AssertionError(f"/lang requested after {before.count('answer response')} answer responses, want 2: {order}")

        with step("6c no dialog; the page is /exam in Indonesian"):
            if dialogs:
                raise AssertionError(f"dialogs appeared: {dialogs}")
            expect(s).to_have_url(base_url + "/exam")
            expect(s.locator("html")).to_have_attribute("lang", "id")

        with step("6d answers kept and saved, timer runs, no banner"):
            if picked(s, 1) != choice:
                raise AssertionError(f"question 1 has {picked(s, 1)}, want {choice}")
            expect(question(s, 2).locator("input.answer-text")).to_have_value("42")
            saved = s.locator("#exam-text").get_attribute("data-all-saved")
            if not saved or saved == english_saved:
                raise AssertionError(f"data-all-saved {saved!r} is not Indonesian")
            expect(s.locator("#save-state")).to_have_text(saved)
            timer_runs(s)
            expect(s.locator("#monitor-banner")).to_be_hidden()
            s.screenshot(path=out / "exam-after-switch.png", animations="disabled")

        with step("6e after 5 s the teacher sees no flag"):
            s.wait_for_timeout(5000)
            expect(s.locator("#monitor-banner")).to_be_hidden()
            if (got := flags(teacher, base_url, exam_id, "Ayu Lestari")) != "0":
                raise AssertionError(f"results row data-flags={got}, want 0")
            if (n := event_rows(teacher, base_url, exam_id, "Ayu Lestari")) != 0:
                raise AssertionError(f"{n} rows in #events, want none")

        with step("6f the monitor still works: a blocked copy shows the Indonesian banner and one flag"):
            copy(s)
            banner = s.locator("#monitor-banner")
            expect(banner).to_be_visible()
            text = banner.inner_text().strip()
            if not text or text == ENGLISH_BANNER:
                raise AssertionError(f"banner text {text!r} is not Indonesian")
            wait_for_flags(teacher, base_url, exam_id, "Ayu Lestari", "1", 5)

        with step("6g back to English with nothing unsaved goes through at once and adds no flag"):
            expect(s.locator("#save-state")).to_have_attribute("data-state", "saved")
            open_switch(s)
            t0 = time.monotonic()
            lang_button(s, "en").click()
            expect(s.locator("html")).to_have_attribute("lang", "en", timeout=5000)
            took = time.monotonic() - t0
            print(f"  switch with nothing unsaved took {took:.3f}s", flush=True)
            if took >= 1:
                raise AssertionError(f"the switch took {took:.2f}s, want under 1s")
            if dialogs:
                raise AssertionError(f"dialogs appeared: {dialogs}")
            s.wait_for_timeout(4000)
            if (got := flags(teacher, base_url, exam_id, "Ayu Lestari")) != "1":
                raise AssertionError(f"results row data-flags={got}, want 1")

        with step("7 a save that cannot finish holds the switch back; after a retry a new press switches"):
            retrying = s.locator("#exam-text").get_attribute("data-retrying")
            s.route("**/exam/answer", lambda route: route.abort())
            question(s, 1).locator("label.option").nth(1).click()
            choice = picked(s, 1)
            navigations = []
            s.on("framenavigated", lambda frame: frame == s.main_frame and navigations.append(frame.url))
            press(s, "id")
            expect(lang_button(s, "id")).to_be_disabled()
            expect(lang_button(s, "id")).to_be_enabled(timeout=6000)
            expect(lang_button(s, "en")).to_be_enabled()
            if navigations:
                raise AssertionError(f"navigated while unsaved: {navigations}")
            expect(s.locator("html")).to_have_attribute("lang", "en")
            expect(s.locator("#save-state")).to_have_text(retrying)
            s.unroute("**/exam/answer")
            expect(s.locator("#save-state")).to_have_attribute("data-state", "saved", timeout=20000)
            press(s, "id")
            expect(s.locator("html")).to_have_attribute("lang", "id", timeout=5000)
            if picked(s, 1) != choice:
                raise AssertionError(f"question 1 has {picked(s, 1)}, want {choice}")
            if dialogs:
                raise AssertionError(f"dialogs appeared: {dialogs}")

        with step("8 the student submits; the result is Indonesian; the teacher's attempt page shows both answers"):
            s.locator("#submit-open").click()
            s.locator("#submit-confirm").click()
            expect(s.locator("main#result")).to_be_visible()
            expect(s.locator("html")).to_have_attribute("lang", "id")
            teacher.goto(base_url + attempt_href(teacher, base_url, exam_id, "Ayu Lestari"))
            expect(teacher.locator("li[data-picked=true] .option-body")).to_have_text("Bandung")
            expect(teacher.locator(".typed-text")).to_have_text("42")

        with step("9 no console errors, page errors or foreign requests"):
            problems = []
            for log in logs:
                # Step 7 aborts answer requests on purpose; checks 2 and 4 load a
                # 404 address and a refused start on purpose.
                expected = {
                    f"{base_url}/no-such-page: Failed to load resource: the server responded with a status of 404 (Not Found)",
                    f"{base_url}/start: Failed to load resource: the server responded with a status of 422 (Unprocessable Entity)",
                }
                problems += [
                    f"console error: {e}"
                    for e in log["console_errors"]
                    if not e.endswith("Failed to load resource: net::ERR_FAILED") and e not in expected
                ]
                problems += [f"page error: {e}" for e in log["page_errors"]]
                problems += [f"foreign request: {u}" for u in foreign_requests(log["requests"], base_url, oidc_url)]
            if problems:
                raise AssertionError("\n  ".join(["problems found:"] + problems))

        browser.close()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True)
    add_sign_in_args(parser)
    parser.add_argument("--out", type=Path, default=Path(__file__).parent / "out" / "language")
    args = parser.parse_args()
    base_url, oidc_url = args.base_url.rstrip("/"), args.oidc_url.rstrip("/")
    require_cross_site(base_url, oidc_url)
    try:
        run(base_url, args.admin_email, oidc_url, args.out)
    except StepFailed as err:
        print(err, file=sys.stderr)
        return 1
    print(f"OK: language check passed; screenshots in {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
