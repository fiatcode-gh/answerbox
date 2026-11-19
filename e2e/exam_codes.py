# /// script
# requires-python = ">=3.12"
# dependencies = ["playwright==1.63.0"]
# ///
"""Browser check for exam codes in real Chromium: two teachers each open an
exam at the same time, students start with the codes (typed or through the
student link) and land in the right exam, and a closed exam's code is refused
exactly like an unknown one. Needs a fresh instance.

uv run e2e/exam_codes.py --base-url URL --admin-email EMAIL --oidc-url URL --out DIR
"""

import argparse
import re
import sys
import tempfile
from contextlib import contextmanager
from pathlib import Path

from playwright.sync_api import Page, expect, sync_playwright

from common import (
    REFUSED,
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
PHONE_VIEWPORT = {"width": 412, "height": 915}
TEACHER1 = "teacher1@example.test"
TEACHER2 = "teacher2@example.test"
BIOLOGY = "Biology check"
HISTORY = "History check"
STUDENT1 = "Ayu Lestari"
STUDENT2 = "Budi Santoso"
STUDENT3 = "Citra Dewi"
STUDENT4 = "Dewi Kusuma"


class StepFailed(Exception):
    pass


@contextmanager
def step(name: str):
    print(f"step: {name}", flush=True)
    try:
        yield
    except Exception as err:  # every failure names its step
        raise StepFailed(f"FAIL [{name}]: {err}") from err


def no_page_overflow(page: Page) -> None:
    widths = page.evaluate(
        "() => [document.documentElement.scrollWidth, document.documentElement.clientWidth]"
    )
    if widths[0] > widths[1]:
        raise AssertionError(f"page overflows horizontally on {page.url}: scrollWidth {widths[0]} > clientWidth {widths[1]}")


def status_badge(page: Page, exam_id: int):
    return page.locator(f"#exams tbody tr[data-exam-id='{exam_id}'] .badge")


def attempt_names(page: Page, base_url: str, exam_id: int) -> str:
    page.goto(f"{base_url}/admin/exams/{exam_id}/attempts")
    expect(page.locator("#attempts")).to_be_visible()
    return page.locator("#attempts tbody").inner_text()


def run(base_url: str, admin_email: str, oidc_url: str, out: Path) -> None:
    out.mkdir(parents=True, exist_ok=True)
    logs = []
    refused_start = (
        f"{base_url}/start: Failed to load resource: "
        "the server responded with a status of 422 (Unprocessable Entity)"
    )
    with sync_playwright() as p, tempfile.TemporaryDirectory() as tmp:
        browser = p.chromium.launch(headless=True)
        phone = p.devices["Pixel 7"]

        def new_context(**kwargs):
            context = browser.new_context(**kwargs)
            logs.append(watch(context, base_url))
            return context

        a = new_context(**DESKTOP).new_page()
        with step("1 admin signs in and adds two teachers"):
            teacher_login(a, base_url, admin_email, "Ada Admin")
            if a.get_by_text("No exams yet.").count() != 1:
                raise AssertionError("not a fresh instance: the exams page does not say 'No exams yet.'")
            a.goto(f"{base_url}/admin/teachers")
            form = a.locator("#add-teacher-form")
            for email in (TEACHER1, TEACHER2):
                form.locator("input[name=email]").fill(email)
                form.locator("button[type=submit]").click()
                expect(a.locator("#teachers tbody tr").filter(has_text=email)).to_have_count(1)

        t1 = new_context(**DESKTOP).new_page()
        with step("2 teacher 1 opens Biology check and sees its code large with the student link"):
            (Path(tmp) / "biology").mkdir()
            biology_zip, _ = build_fixture_zip(Path(tmp) / "biology", title=BIOLOGY)
            teacher_login(t1, base_url, TEACHER1, "Tia One")
            biology_id = import_and_open(t1, biology_zip)
            code1 = exam_code(t1, biology_id)
            t1.goto(f"{base_url}/admin/exams/{biology_id}")
            expect(t1.locator("#exam-code")).to_have_text(code1)
            size = t1.locator("#exam-code").evaluate("e => parseFloat(getComputedStyle(e).fontSize)")
            if size < 96:
                raise AssertionError(f"exam code font-size is {size}px, want at least 96px")
            link = f"{base_url}/?code={code1}"
            expect(t1.locator("#student-link")).to_have_text(link)
            expect(t1.locator("#student-link")).to_have_attribute("href", link)
            t1.screenshot(path=out / "desktop-exam-code.png", full_page=True, animations="disabled")
            t1.set_viewport_size(PHONE_VIEWPORT)
            no_page_overflow(t1)
            t1.screenshot(path=out / "phone-exam-code.png", full_page=True, animations="disabled")
            t1.set_viewport_size(DESKTOP["viewport"])

        with step("2b teacher 1 copies the student link with Copy link, by pointer and by keyboard"):
            t1.context.grant_permissions(["clipboard-read", "clipboard-write"])
            copy = t1.locator("#copy-link")
            expect(copy).to_be_visible()
            copy.click()
            expect(t1.locator("#copy-status")).to_have_text("Copied")
            if (copied := t1.evaluate("navigator.clipboard.readText()")) != link:
                raise AssertionError(f"clipboard holds {copied!r}, want {link!r}")
            t1.evaluate("navigator.clipboard.writeText('')")
            copy.focus()
            t1.keyboard.press("Enter")
            expect(t1.locator("#copy-status")).to_have_text("Copied")
            if (copied := t1.evaluate("navigator.clipboard.readText()")) != link:
                raise AssertionError(f"after Enter the clipboard holds {copied!r}, want {link!r}")

        t2 = new_context(**DESKTOP).new_page()
        with step("3 teacher 2 opens History check; both exams are open, each teacher sees only their own"):
            (Path(tmp) / "history").mkdir()
            history_zip, _ = build_fixture_zip(Path(tmp) / "history", title=HISTORY)
            teacher_login(t2, base_url, TEACHER2, "Tia Two")
            history_id = import_and_open(t2, history_zip)
            code2 = exam_code(t2, history_id)
            if code2 == code1:
                raise AssertionError(f"both exams have the code {code1}")
            t1.goto(f"{base_url}/admin/exams")
            expect(status_badge(t1, biology_id)).to_have_text("Open")
            expect(t1.locator("#exams")).not_to_contain_text(code2)
            a.goto(f"{base_url}/admin/exams")
            for exam_id, code in ((biology_id, code1), (history_id, code2)):
                expect(status_badge(a, exam_id)).to_have_text("Open")
                expect(a.locator(f"#exams tbody tr[data-exam-id='{exam_id}'] td.code")).to_have_text(code)

        s1 = new_context(**phone).new_page()
        with step("4 student 1 types teacher 1's code loosely and lands in Biology check"):
            s1.goto(base_url + "/")
            expect(s1.locator("h1")).to_have_text("Answerbox")
            for title in (BIOLOGY, HISTORY):
                expect(s1.locator("body")).not_to_contain_text(title)
            start(s1, base_url, " " + code1[:3].lower() + "-" + code1[3:].lower() + " ", STUDENT1)
            expect(s1.locator(".exam-title")).to_have_text(BIOLOGY)

        s2 = new_context(**phone).new_page()
        with step("5 student 2 follows teacher 2's student link and lands in History check"):
            t2.goto(f"{base_url}/admin/exams/{history_id}")
            href = t2.locator("#student-link").get_attribute("href")
            s2.goto(href)
            expect(s2.locator("h1")).to_have_text(HISTORY)
            expect(s2.locator("#start-code")).to_have_value(code2)
            expect(s2.locator("#start-form input[name=name]")).to_be_focused()
            s2.locator("#start-form input[name=name]").fill(STUDENT2)
            s2.locator("#start-form button[type=submit]").click()
            expect(s2.locator("main#exam")).to_be_visible()
            expect(s2.locator(".exam-title")).to_have_text(HISTORY)

        with step("6 each teacher's results list only their exam's student"):
            names = attempt_names(t1, base_url, biology_id)
            if STUDENT1 not in names or STUDENT2 in names:
                raise AssertionError(f"teacher 1's attempts: {names!r}")
            names = attempt_names(t2, base_url, history_id)
            if STUDENT2 not in names or STUDENT1 in names:
                raise AssertionError(f"teacher 2's attempts: {names!r}")
            response = t1.context.request.get(f"{base_url}/admin/exams/{history_id}", max_redirects=0)
            if response.status != 404:
                raise AssertionError(f"teacher 1 got {response.status} for teacher 2's exam, want 404")

        s3 = new_context(**phone).new_page()
        with step("7 teacher 1 closes Biology check; its code is refused and the name is kept"):
            t1.goto(f"{base_url}/admin/exams")
            t1.locator(f"form[action='/admin/exams/{biology_id}/close'] button[type=submit]").click()
            expect(t1).to_have_url(re.compile(r"/admin/exams$"))
            expect(status_badge(t1, biology_id)).to_have_text("Closed")
            s3.goto(base_url + "/")
            form = s3.locator("#start-form")
            form.locator("input[name=code]").fill(code1)
            form.locator("input[name=name]").fill(STUDENT3)
            form.locator("button[type=submit]").click()
            expect(s3.locator("[role=alert]")).to_have_text(REFUSED)
            expect(s3.locator("main#landing")).to_be_visible()
            expect(s3.locator("#start-form input[name=name]")).to_have_value(STUDENT3)
            expect(s3.locator("main#exam")).to_have_count(0)
            s3.screenshot(path=out / "phone-refused.png", full_page=True, animations="disabled")

        with step("8 a link with the closed code looks exactly like one with an unknown code"):
            response = s3.goto(f"{base_url}/?code={code1}")
            if response.status != 200:
                raise AssertionError(f"/?code=<closed code> answered {response.status}, want 200")
            expect(s3.locator("[role=alert]")).to_have_text(REFUSED)
            expect(s3.locator("h1")).to_have_text("Answerbox")
            expect(s3.locator("body")).not_to_contain_text(BIOLOGY)
            closed_html = s3.locator("main#landing").inner_html()
            unknown = next(c for c in ("ZZZZZZ", "YYYYYY") if c not in (code1, code2))
            response = s3.goto(f"{base_url}/?code={unknown}")
            if response.status != 200:
                raise AssertionError(f"/?code=<unknown code> answered {response.status}, want 200")
            unknown_html = s3.locator("main#landing").inner_html()
            if unknown_html != closed_html:
                raise AssertionError(f"closed and unknown codes differ:\n{closed_html}\n---\n{unknown_html}")

        with step("9 student 2, in progress, following the closed code is sent back to History check"):
            s2.goto(f"{base_url}/?code={code1}")
            expect(s2).to_have_url(f"{base_url}/exam")
            expect(s2.locator("main#exam")).to_be_visible()
            expect(s2.locator(".exam-title")).to_have_text(HISTORY)

        with step("10 History check stays open: student 4 starts with its code"):
            s4 = new_context(**phone).new_page()
            start(s4, base_url, code2.upper(), STUDENT4)
            expect(s4.locator(".exam-title")).to_have_text(HISTORY)

        with step("11 no page errors or foreign requests; the refused start is the only console error"):
            problems = []
            console = []
            for log in logs:
                console += log["console_errors"]
                problems += [f"page error: {e}" for e in log["page_errors"]]
                problems += [f"foreign request: {u}" for u in foreign_requests(log["requests"], base_url, oidc_url)]
            if console != [refused_start]:
                problems.append(f"console errors: want exactly [{refused_start!r}], got {console}")
            if problems:
                raise AssertionError("\n  ".join(["problems found:"] + problems))

        browser.close()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True)
    add_sign_in_args(parser)
    parser.add_argument("--out", type=Path, default=Path(__file__).parent / "out" / "exam-codes")
    args = parser.parse_args()
    base_url, oidc_url = args.base_url.rstrip("/"), args.oidc_url.rstrip("/")
    require_cross_site(base_url, oidc_url)
    try:
        run(base_url, args.admin_email, oidc_url, args.out)
    except StepFailed as err:
        print(err, file=sys.stderr)
        return 1
    print(f"OK: exam codes check passed; screenshots in {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
