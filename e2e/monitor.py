# /// script
# requires-python = ">=3.12"
# dependencies = ["playwright==1.63.0"]
# ///
"""Browser check for the anti-cheat monitor in real Chromium.

uv run e2e/monitor.py --base-url URL --admin-email EMAIL --oidc-url URL --out DIR
"""

import argparse
import re
import sys
import tempfile
import time
from contextlib import contextmanager
from pathlib import Path
from urllib.parse import parse_qs

from playwright.sync_api import Page, expect, sync_playwright
from playwright.sync_api import TimeoutError as PlaywrightTimeout

from common import (
    add_sign_in_args,
    build_fixture_zip,
    exam_code,
    foreign_requests,
    import_and_open,
    require_cross_site,
    start,
    teacher_login,
    watch,
)

DESKTOP = {"viewport": {"width": 1280, "height": 800}}
BANNER = "Your teacher has been notified."
BANNER_HTML = '<div id="monitor-banner" class="banner banner-warn" role="alert" hidden>' + BANNER + "</div>"


class StepFailed(Exception):
    pass


@contextmanager
def step(name: str):
    print(f"step: {name}", flush=True)
    try:
        yield
    except Exception as err:  # every failure names its step
        raise StepFailed(f"FAIL [{name}]: {err}") from err


def question(page: Page, n: int):
    return page.locator(f"section.question#q{n}")


def pick_and_save(page: Page, n: int, index: int) -> None:
    question(page, n).locator("label.option").nth(index).click()
    expect(page.locator("#save-state")).to_have_attribute("data-state", "saved")
    expect(question(page, n).locator(".q-state")).to_have_attribute("data-state", "saved")


def checked(page: Page) -> list:
    return page.eval_on_selector_all(
        "section.question",
        "ss => ss.map(s => (s.querySelector('input[type=radio]:checked') || {}).value || null)",
    )


def hide_banner(page: Page) -> None:
    """Hide the banner again so the next event must show it anew."""
    page.evaluate("document.getElementById('monitor-banner').hidden = true")


# Hidden-page simulation: headless Chromium keeps every page visible, so the
# page's visibilityState is overridden and visibilitychange dispatched, which
# is exactly what a tab or app switch delivers to the page.
HIDE_JS = """() => {
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "hidden" });
  Object.defineProperty(document, "hidden", { configurable: true, get: () => true });
  document.dispatchEvent(new Event("visibilitychange"));
}"""
SHOW_JS = """() => {
  delete document.visibilityState;
  delete document.hidden;
  document.dispatchEvent(new Event("visibilitychange"));
}"""


def leave_page(page: Page, ms: int) -> None:
    page.evaluate(HIDE_JS)
    page.wait_for_timeout(ms)
    page.evaluate(SHOW_JS)


# Probes run after the page's own handlers (window is the last bubbling
# target) and record whether the page blocked the action.
PROBE_JS = """() => {
  window.__blocked = {};
  for (const type of ["paste", "contextmenu"]) {
    window.addEventListener(type, e => { window.__blocked[type] = e.defaultPrevented; });
  }
}"""


def paste(page: Page) -> bool:
    """Press Ctrl+V; returns whether the paste was blocked."""
    page.locator("section.question#q3 .question-body").click()
    page.keyboard.press("Control+V")
    try:
        page.wait_for_function("'paste' in window.__blocked", timeout=2000)
    except PlaywrightTimeout:
        # Some Chromium builds fire no paste event without clipboard content;
        # deliver the event the browser would.
        print("  note: Ctrl+V fired no paste event; dispatching one", flush=True)
        page.evaluate(
            "document.querySelector('section.question#q3 .question-body')"
            ".dispatchEvent(new ClipboardEvent('paste', {bubbles: true, cancelable: true}))"
        )
    return page.evaluate("window.__blocked.paste")


def wait_for_events(teacher: Page, base_url: str, exam_id: int, name: str, want: list[str]) -> None:
    """Reload the teacher's attempt detail until its events equal want (5 s)."""
    deadline = time.monotonic() + 5
    while True:
        got = event_texts(teacher, base_url, exam_id, name)
        if got == want:
            return
        if time.monotonic() > deadline:
            raise AssertionError(f"teacher sees events {got}, want {want}")
        teacher.wait_for_timeout(250)


def flag_count(teacher: Page, base_url: str, exam_id: int, name: str) -> int:
    return int(attempt_row(teacher, base_url, exam_id, name).locator("td.flags").inner_text())


def wait_for_count(teacher: Page, base_url: str, exam_id: int, name: str, want: int, seconds: float) -> None:
    deadline = time.monotonic() + seconds
    while (got := flag_count(teacher, base_url, exam_id, name)) != want:
        if time.monotonic() > deadline:
            raise AssertionError(f"teacher sees {got} flags, want {want}")
        teacher.wait_for_timeout(500)


def right_click(page: Page) -> bool:
    page.locator("section.question#q4 .question-body").click(button="right")
    page.wait_for_function("'contextmenu' in window.__blocked")
    return page.evaluate("window.__blocked.contextmenu")


def show_tail_of_question_3(page: Page) -> None:
    """Scroll so question 3's last 100 px stay visible below the bar, with question 4's text on screen."""
    page.evaluate(
        """() => {
            const bar = document.querySelector(".exam-bar").getBoundingClientRect().bottom;
            const q3 = document.querySelector("section.question#q3").getBoundingClientRect().bottom;
            window.scrollBy(0, q3 - bar - 100);
        }"""
    )


def scroll_rule_question(page: Page) -> str:
    """The element id of the question the scroll-position rule names."""
    return page.evaluate(
        """() => {
            const bar = document.querySelector(".exam-bar").getBoundingClientRect().bottom;
            return [...document.querySelectorAll("section.question")]
                .find(s => s.getBoundingClientRect().bottom > bar).id;
        }"""
    )


def page_position(page: Page) -> dict:
    return page.evaluate(
        "() => ({scrollY: window.scrollY, q4: document.querySelector('section.question#q4').getBoundingClientRect().y})"
    )


def moved(a: dict, b: dict) -> bool:
    return any(abs(a[k] - b[k]) > 0.5 for k in a)


def attempt_row(teacher: Page, base_url: str, exam_id: int, name: str):
    teacher.goto(f"{base_url}/admin/exams/{exam_id}/attempts")
    return teacher.locator("#attempts tbody tr", has_text=name)


def event_texts(teacher: Page, base_url: str, exam_id: int, name: str) -> list[str]:
    row = attempt_row(teacher, base_url, exam_id, name)
    row.get_by_role("link", name="View").click()
    expect(teacher.locator("#flags")).to_be_visible()
    return [t.strip() for t in teacher.locator("#events tr.event td:nth-child(2)").all_inner_texts()]


def run(base_url: str, email: str, oidc_url: str, out: Path) -> None:
    out.mkdir(parents=True, exist_ok=True)
    logs = []
    with sync_playwright() as p, tempfile.TemporaryDirectory() as tmp:
        browser = p.chromium.launch(headless=True)

        def new_context(**kwargs):
            context = browser.new_context(**kwargs)
            logs.append(watch(context, base_url))
            return context

        teacher = new_context(**DESKTOP).new_page()
        with step("1 teacher imports and opens a default (monitor on) exam"):
            (Path(tmp) / "on").mkdir()
            zip_on, key = build_fixture_zip(Path(tmp) / "on", title="Monitor on")
            teacher_login(teacher, base_url, email)
            on_id = import_and_open(teacher, zip_on)
            on_code = exam_code(teacher, on_id)
            teacher.goto(f"{base_url}/admin/exams/{on_id}")
            expect(teacher.locator("dl.settings")).to_contain_text(re.compile(r"Monitor\s*On"))

        s1 = new_context(**DESKTOP).new_page()
        with step("2 student starts; no banner; an answer saves"):
            start(s1, base_url, on_code, "Ayu Lestari")
            expect(s1.locator("main#exam")).to_have_attribute("data-monitor", "on")
            expect(s1.locator("#monitor-banner")).to_be_hidden()
            s1.evaluate(PROBE_JS)
            pick_and_save(s1, 1, 0)

        with step("3 leaving for 1 s is not flagged"):
            leave_page(s1, 1000)
            s1.wait_for_timeout(500)
            expect(s1.locator("#monitor-banner")).to_be_hidden()

        with step("4 leaving for more than 3 s shows the banner and the teacher sees it"):
            leave_page(s1, 3600)
            expect(s1.locator("#monitor-banner")).to_be_visible()
            expect(s1.locator("#monitor-banner")).to_have_text(BANNER)
            s1.screenshot(path=out / "monitor-banner.png", animations="disabled")
            wait_for_events(teacher, base_url, on_id, "Ayu Lestari", ["Left the exam page"])

        with step("5 a paste is blocked, shows the banner and is recorded"):
            hide_banner(s1)
            if not paste(s1):
                raise AssertionError("paste was not blocked")
            expect(s1.locator("#monitor-banner")).to_be_visible()
            wait_for_events(teacher, base_url, on_id, "Ayu Lestari", ["Left the exam page", "Tried to paste"])

        with step("6 a right-click on question 4's text is blocked, flags question 4 and shows the banner without moving the page"):
            expect(s1.locator("#monitor-banner")).to_be_hidden(timeout=8000)  # resting layout
            show_tail_of_question_3(s1)
            if scroll_rule_question(s1) != "q3":
                raise AssertionError(f"setup: the scroll rule names {scroll_rule_question(s1)}, want q3")
            q4_id = question(s1, 4).get_attribute("data-question-id")
            before = page_position(s1)
            with s1.expect_request("**/exam/event") as sent:
                if not right_click(s1):
                    raise AssertionError("context menu was not blocked")
            named = parse_qs(sent.value.post_data).get("question")
            if named != [q4_id]:
                raise AssertionError(f"the event names question id {named}, want [{q4_id}] (question 4)")
            expect(s1.locator("#monitor-banner")).to_be_visible()
            if moved(before, page_position(s1)):
                raise AssertionError(f"showing the banner moved the page: {before} → {page_position(s1)}")
            texts = event_texts(teacher, base_url, on_id, "Ayu Lestari")
            if texts != ["Left the exam page", "Tried to paste", "Right-clicked"]:
                raise AssertionError(f"events = {texts}")
            body = question(s1, 4).locator(".question-body").inner_text()
            file_position = next(i for i, text in enumerate(key, start=1) if text in body)
            cell = teacher.locator("#events tr.event").nth(2).locator("td:nth-child(3)")
            expect(cell).to_have_text(f"Question 4 on screen (file question {file_position})")
            expect(cell.locator("a")).to_have_attribute("href", f"#q{file_position}")
            questions = teacher.locator("#events tr.event td:nth-child(3)").all_inner_texts()
            if not all(q.startswith("Question ") for q in questions):
                raise AssertionError(f"event questions = {questions}")
            teacher.screenshot(path=out / "teacher-flags.png", full_page=True, animations="disabled")
            row = attempt_row(teacher, base_url, on_id, "Ayu Lestari")
            expect(row.locator("td.flags")).to_have_text("3")

        with step("6a the banner hides by itself, again without moving the page"):
            expect(s1.locator("#monitor-banner")).to_be_hidden(timeout=8000)
            if moved(before, page_position(s1)):
                raise AssertionError(f"hiding the banner moved the page: {before} → {page_position(s1)}")

        with step("6b a height-only resize on desktop is flagged and re-shows the banner"):
            s1.set_viewport_size({"width": 1280, "height": 700})
            expect(s1.locator("#monitor-banner")).to_be_visible(timeout=4000)
            shown = time.monotonic()
            expect(s1.locator("#monitor-banner")).to_have_text(BANNER)
            wait_for_events(
                teacher, base_url, on_id, "Ayu Lestari",
                ["Left the exam page", "Tried to paste", "Right-clicked", "Resized the window"],
            )
            expect(s1.locator("#monitor-banner")).to_be_hidden(timeout=8000)
            took = time.monotonic() - shown
            if not 4 <= took <= 7:
                raise AssertionError(f"banner hid after {took:.1f}s, want about 5s")
            s1.set_viewport_size({"width": 1280, "height": 800})
            expect(s1.locator("#monitor-banner")).to_be_visible(timeout=4000)

        with step("6c while shown, the banner covers neither a jumped-to question heading nor the navigator"):
            s1.locator("#question-nav a.bubble[href='#q5']").click()
            expect(question(s1, 5)).to_be_in_viewport()
            s1.wait_for_timeout(300)
            expect(s1.locator("#monitor-banner")).to_be_visible()
            banner = s1.locator("#monitor-banner").bounding_box()
            head = question(s1, 5).locator(".question-head").bounding_box()
            nav = s1.locator("#question-nav").bounding_box()
            bottom = banner["y"] + banner["height"]
            if head["y"] < bottom or nav["y"] < bottom:
                raise AssertionError(f"banner bottom {bottom} overlaps heading y={head['y']} or nav y={nav['y']}")
            s1.screenshot(path=out / "monitor-banner-jump.png", animations="disabled")

        with step("6d a failing event endpoint gets one backed-off request chain, not one per event"):
            event_posts = []

            def fail(route):
                event_posts.append(route.request.url)
                route.fulfill(status=503, body="unavailable")

            s1.route("**/exam/event", fail)
            for _ in range(5):
                s1.locator("section.question#q4 .question-body").click(button="right")
            s1.wait_for_timeout(4000)
            if not 1 <= len(event_posts) <= 4:
                raise AssertionError(f"{len(event_posts)} event requests in 4 s while failing, want 1 to 4")
            s1.unroute("**/exam/event")
            wait_for_count(teacher, base_url, on_id, "Ayu Lestari", 10, 15)

        with step("7 answers still save, survive a reload and submit"):
            pick_and_save(s1, 2, 1)
            before = checked(s1)
            s1.reload()
            expect(s1.locator("main#exam")).to_be_visible()
            if checked(s1) != before:
                raise AssertionError(f"picks changed on reload: {before} → {checked(s1)}")
            s1.locator("#submit-open").click()
            s1.locator("#submit-confirm").click()
            expect(s1.locator("main#result")).to_be_visible()
            expect(s1.locator("main#result")).to_have_attribute("data-status", "submitted")
            row = attempt_row(teacher, base_url, on_id, "Ayu Lestari")
            expect(row).to_contain_text("Submitted")
            expect(row.locator("td.flags")).to_have_text("10")

        with step("7a without the banner element the monitor stays off and the exam page still works"):
            s3 = new_context(**DESKTOP).new_page()
            start(s3, base_url, on_code, "Citra Dewi")

            def without_banner(route):
                response = route.fetch()
                route.fulfill(response=response, body=response.text().replace(BANNER_HTML, ""))

            s3.route(re.compile(r"/exam$"), without_banner)
            s3.goto(base_url + "/exam")  # the redirect after /start is not routed; a direct load is
            expect(s3.locator("main#exam")).to_be_visible()
            expect(s3.locator("#monitor-banner")).to_have_count(0)
            first = s3.locator("#timer").inner_text()
            s3.wait_for_timeout(2300)
            if s3.locator("#timer").inner_text() == first:
                raise AssertionError("the countdown is not running")
            pick_and_save(s3, 1, 0)
            s3.locator("#submit-open").click()
            expect(s3.locator("#submit-dialog")).to_be_visible()
            s3.locator("#submit-cancel").click()

        with step("8 teacher imports and opens a monitor: off exam"):
            (Path(tmp) / "off").mkdir()
            zip_off, _ = build_fixture_zip(Path(tmp) / "off", title="Monitor off", monitor="off")
            teacher.goto(f"{base_url}/admin/exams")
            off_id = import_and_open(teacher, zip_off)
            off_code = exam_code(teacher, off_id)
            teacher.goto(f"{base_url}/admin/exams/{off_id}")
            expect(teacher.locator("dl.settings")).to_contain_text(re.compile(r"Monitor\s*Off"))

        s2_context = new_context(**DESKTOP)
        event_posts = []
        s2_context.on("request", lambda r: r.url.endswith("/exam/event") and event_posts.append(r.url))
        s2 = s2_context.new_page()
        with step("9 monitor off: leaving, paste and right-click are neither blocked nor flagged"):
            start(s2, base_url, off_code, "Budi Santoso")
            expect(s2.locator("main#exam")).to_have_attribute("data-monitor", "off")
            s2.evaluate(PROBE_JS)
            pick_and_save(s2, 1, 0)
            leave_page(s2, 3600)
            if paste(s2):
                raise AssertionError("paste was blocked with the monitor off")
            if right_click(s2):
                raise AssertionError("context menu was blocked with the monitor off")
            s2.wait_for_timeout(1000)
            expect(s2.locator("#monitor-banner")).to_be_hidden()
            if event_posts:
                raise AssertionError(f"event requests sent: {event_posts}")
            row = attempt_row(teacher, base_url, off_id, "Budi Santoso")
            expect(row.locator("td.flags")).to_have_text("0")

        with step("10 monitor off: answers save, survive a reload and submit"):
            pick_and_save(s2, 2, 1)
            before = checked(s2)
            s2.reload()
            expect(s2.locator("main#exam")).to_be_visible()
            if checked(s2) != before:
                raise AssertionError(f"picks changed on reload: {before} → {checked(s2)}")
            s2.locator("#submit-open").click()
            s2.locator("#submit-confirm").click()
            expect(s2.locator("main#result")).to_have_attribute("data-status", "submitted")

        with step("11 no console errors, page errors or foreign requests"):
            problems = []
            for log in logs:
                # Step 6d answers event requests with 503 on purpose.
                problems += [
                    f"console error: {e}"
                    for e in log["console_errors"]
                    if not e.endswith("the server responded with a status of 503 (Service Unavailable)")
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
    parser.add_argument("--out", type=Path, default=Path(__file__).parent / "out" / "monitor")
    args = parser.parse_args()
    base_url, oidc_url = args.base_url.rstrip("/"), args.oidc_url.rstrip("/")
    require_cross_site(base_url, oidc_url)
    try:
        run(base_url, args.admin_email, oidc_url, args.out)
    except StepFailed as err:
        print(err, file=sys.stderr)
        return 1
    print(f"OK: monitor check passed; screenshots in {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
