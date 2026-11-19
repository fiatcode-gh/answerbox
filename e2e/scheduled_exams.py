# /// script
# requires-python = ">=3.12"
# dependencies = ["playwright==1.63.0"]
# ///
"""Browser check for exam schedules in real Chromium, on the real clock: a
teacher schedules a 5-minute exam to open at the next whole minute and close a
minute later; a student who comes early sees when it opens, starts once it is
open and gets 5 minutes; after the close time the code is refused exactly like
an unknown one while the started attempt goes on; Open removes the schedule.
Needs a fresh instance whose ANSWERBOX_TIME_ZONE is --time-zone. Waits about two
minutes.

uv run e2e/scheduled_exams.py --base-url URL --admin-email EMAIL --oidc-url URL --time-zone ZONE --out DIR
"""

import argparse
import re
import sys
import tempfile
import time
from contextlib import contextmanager
from datetime import datetime, timedelta
from pathlib import Path
from zoneinfo import ZoneInfo

from playwright.sync_api import Page, expect, sync_playwright

from common import (
    REFUSED,
    add_sign_in_args,
    build_fixture_zip,
    exam_code,
    foreign_requests,
    import_exam,
    require_cross_site,
    start,
    teacher_login,
    watch,
)

DESKTOP = {"viewport": {"width": 1280, "height": 800}}
PHONE_VIEWPORT = {"width": 412, "height": 915}
TITLE = "Schedule check"
STUDENT1 = "Eka Pratama"
STUDENT2 = "Fajar Nugroho"
STUDENT3 = "Gita Maharani"
INPUT_LAYOUT = "%Y-%m-%dT%H:%M"
SHOWN_LAYOUT = "%Y-%m-%d %H:%M"


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


def row(page: Page, exam_id: int):
    return page.locator(f"#exams tbody tr[data-exam-id='{exam_id}']")


def list_badge(page: Page, base_url: str, exam_id: int, want: str) -> None:
    page.goto(f"{base_url}/admin/exams")
    expect(row(page, exam_id).locator(".badge")).to_have_text(want)


def sleep_until(at: datetime, zone: ZoneInfo) -> float:
    """Sleep until at; returns the seconds slept."""
    seconds = max(0.0, (at - datetime.now(zone)).total_seconds())
    print(f"  waiting {seconds:.1f} s until {at.strftime('%H:%M:%S')}", flush=True)
    time.sleep(seconds)
    return seconds


def press_start(page: Page) -> None:
    """Press Start on the landing page and wait for the answer to load."""
    with page.expect_navigation():
        page.locator("#start-form button[type=submit]").click()


def remaining_ms(page: Page) -> int:
    return int(page.locator("main#exam").get_attribute("data-remaining-ms"))


def run(base_url: str, admin_email: str, oidc_url: str, tz: str, out: Path) -> float:
    out.mkdir(parents=True, exist_ok=True)
    zone = ZoneInfo(tz)
    logs = []
    waited = 0.0
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

        t = new_context(**DESKTOP).new_page()
        with step("1 teacher imports a 5-minute exam; its page shows the length and the server's zone"):
            teacher_login(t, base_url, admin_email, "Ada Admin")
            if t.get_by_text("No exams yet.").count() != 1:
                raise AssertionError("not a fresh instance: the exams page does not say 'No exams yet.'")
            zip_path, _ = build_fixture_zip(tmp, title=TITLE, monitor="off", duration=5)
            exam_id = import_exam(t, zip_path)
            exam_page = f"{base_url}/admin/exams/{exam_id}"
            expect(t.locator("dl.settings dt").first).to_have_text("Length")
            expect(t.locator("dl.settings dd").first).to_have_text("5 minutes")
            if f"Times are shown in {tz}." not in t.locator(".zone-note").inner_text():
                raise AssertionError(f"the server's time zone is not {tz}")

        with step("2 teacher schedules it to open at the next whole minute and close a minute later"):
            base = datetime.now(zone) + timedelta(seconds=25)
            opens = base.replace(second=0, microsecond=0)
            if opens < base:
                opens += timedelta(minutes=1)
            closes = opens + timedelta(minutes=1)
            print(f"  opens {opens.isoformat()}, closes {closes.isoformat()}", flush=True)
            t.locator("#schedule-details > summary").click()
            form = t.locator("#schedule-form")
            form.locator("input[name=opens_at]").fill(opens.strftime(INPUT_LAYOUT))
            form.locator("input[name=closes_at]").fill(closes.strftime(INPUT_LAYOUT))
            form.locator("button[type=submit]").click()
            expect(t).to_have_url(exam_page)
            expect(t.locator(".exam-head .badge")).to_have_text("Closed")
            expect(t.locator("#schedule-times")).to_contain_text(f"Opens {opens.strftime(SHOWN_LAYOUT)}")
            expect(t.locator("#schedule-times")).to_contain_text(f"Closes {closes.strftime(SHOWN_LAYOUT)}")
            expect(t.locator("#schedule-opens")).to_have_value(opens.strftime(INPUT_LAYOUT))
            expect(t.locator("#schedule-closes")).to_have_value(closes.strftime(INPUT_LAYOUT))
            expect(t.locator(f"form[action='/admin/exams/{exam_id}/open']")).to_have_count(1)
            expect(t.locator(f"form[action='/admin/exams/{exam_id}/close']")).to_have_count(1)
            t.screenshot(path=out / "desktop-exam-schedule.png", full_page=True, animations="disabled")
            t.set_viewport_size(PHONE_VIEWPORT)
            no_page_overflow(t)
            t.screenshot(path=out / "phone-exam-schedule.png", full_page=True, animations="disabled")
            t.set_viewport_size(DESKTOP["viewport"])
            list_badge(t, base_url, exam_id, "Closed")
            expect(row(t, exam_id)).to_contain_text(f"Opens {opens.strftime(SHOWN_LAYOUT)}")
            code = exam_code(t, exam_id)
            t.screenshot(path=out / "desktop-exams-schedule.png", full_page=True, animations="disabled")
            t.set_viewport_size(PHONE_VIEWPORT)
            no_page_overflow(t)
            t.screenshot(path=out / "phone-exams-schedule.png", full_page=True, animations="disabled")
            t.set_viewport_size(DESKTOP["viewport"])

        s1 = new_context(**phone).new_page()
        with step("3 an early student sees the title and when the exam opens, and cannot start yet"):
            response = s1.goto(f"{base_url}/?code={code}")
            if response.status != 200:
                raise AssertionError(f"/?code=<not yet open code> answered {response.status}, want 200")
            expect(s1.locator("h1")).to_have_text(TITLE)
            alert = re.compile(rf"^This exam opens at {opens.strftime('%H:%M')}( on .+)?\.$")
            expect(s1.locator("[role=alert]")).to_have_text(alert)
            early_alert = s1.locator("[role=alert]").inner_text()
            expect(s1.locator(".landing-note")).to_contain_text("You have 5 minutes.")
            expect(s1.locator("#start-code")).to_have_value(code)
            s1.screenshot(path=out / "phone-early.png", full_page=True, animations="disabled")
            s1.locator("#start-form input[name=name]").fill(STUDENT1)
            press_start(s1)
            expect(s1.locator("main#landing")).to_be_visible()
            expect(s1.locator("[role=alert]")).to_have_text(early_alert)
            expect(s1.locator("#start-form input[name=name]")).to_have_value(STUDENT1)
            expect(s1.locator("main#exam")).to_have_count(0)

        with step("4 at the open time the exams list shows the exam open"):
            waited += sleep_until(opens + timedelta(seconds=1), zone)
            list_badge(t, base_url, exam_id, "Open")

        with step("5 the early student presses Start again and gets 5 minutes"):
            press_start(s1)
            expect(s1.locator("main#exam")).to_be_visible()
            first_remaining = remaining_ms(s1)
            if not 295000 < first_remaining <= 300000:
                raise AssertionError(f"data-remaining-ms is {first_remaining}, want above 295000 and at most 300000")
            expect(s1.locator("#timer")).to_have_text(re.compile(r"^(05:00|04:5\d)$"))
            s1.screenshot(path=out / "phone-exam-5-minutes.png", full_page=True, animations="disabled")

        s2 = new_context(**phone).new_page()
        with step("6 after the close time the code is refused like an unknown one; the started attempt goes on"):
            waited += sleep_until(closes + timedelta(seconds=1), zone)
            list_badge(t, base_url, exam_id, "Closed")
            response = s2.goto(f"{base_url}/?code={code}")
            if response.status != 200:
                raise AssertionError(f"/?code=<closed code> answered {response.status}, want 200")
            expect(s2.locator("[role=alert]")).to_have_text(REFUSED)
            expect(s2.locator("h1")).to_have_text("Answerbox")
            expect(s2.locator("body")).not_to_contain_text(TITLE)
            closed_html = s2.locator("main#landing").inner_html()
            unknown = next(c for c in ("ZZZZZZ", "YYYYYY") if c != code)
            response = s2.goto(f"{base_url}/?code={unknown}")
            if response.status != 200:
                raise AssertionError(f"/?code=<unknown code> answered {response.status}, want 200")
            unknown_html = s2.locator("main#landing").inner_html()
            if unknown_html != closed_html:
                raise AssertionError(f"closed and unknown codes differ:\n{closed_html}\n---\n{unknown_html}")
            s2.goto(base_url + "/")
            form = s2.locator("#start-form")
            form.locator("input[name=code]").fill(code)
            form.locator("input[name=name]").fill(STUDENT2)
            press_start(s2)
            expect(s2.locator("[role=alert]")).to_have_text(REFUSED)
            expect(s2.locator("main#exam")).to_have_count(0)
            s1.reload()
            expect(s1.locator("main#exam")).to_be_visible()
            later_remaining = remaining_ms(s1)
            if not later_remaining < first_remaining:
                raise AssertionError(f"data-remaining-ms went from {first_remaining} to {later_remaining}, want lower")

        s3 = new_context(**phone).new_page()
        with step("7 Open removes the schedule and a third student starts"):
            t.goto(exam_page)
            t.locator(f"form[action='/admin/exams/{exam_id}/open'] button[type=submit]").click()
            expect(t).to_have_url(exam_page)
            expect(t.locator(".exam-head .badge")).to_have_text("Open")
            expect(t.locator("#schedule-times")).to_contain_text("No schedule.")
            expect(t.locator("#schedule-opens")).to_have_value("")
            expect(t.locator("#schedule-closes")).to_have_value("")
            expect(t.locator(f"form[action='/admin/exams/{exam_id}/open']")).to_have_count(0)
            expect(t.locator(f"form[action='/admin/exams/{exam_id}/close']")).to_have_count(1)
            list_badge(t, base_url, exam_id, "Open")
            start(s3, base_url, code, STUDENT3)
            expect(s3.locator(".exam-title")).to_have_text(TITLE)

        with step("8 no page errors or foreign requests; the early and the refused start are the only console errors"):
            problems = []
            console = []
            for log in logs:
                console += log["console_errors"]
                problems += [f"page error: {e}" for e in log["page_errors"]]
                problems += [f"foreign request: {u}" for u in foreign_requests(log["requests"], base_url, oidc_url)]
            if console != [refused_start, refused_start]:
                problems.append(f"console errors: want exactly two of {refused_start!r}, got {console}")
            if problems:
                raise AssertionError("\n  ".join(["problems found:"] + problems))

        browser.close()
    return waited


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True)
    add_sign_in_args(parser)
    parser.add_argument("--time-zone", required=True, help="IANA zone name, the server's ANSWERBOX_TIME_ZONE")
    parser.add_argument("--out", type=Path, default=Path(__file__).parent / "out" / "scheduled-exams")
    args = parser.parse_args()
    base_url, oidc_url = args.base_url.rstrip("/"), args.oidc_url.rstrip("/")
    require_cross_site(base_url, oidc_url)
    try:
        waited = run(base_url, args.admin_email, oidc_url, args.time_zone, args.out)
    except StepFailed as err:
        print(err, file=sys.stderr)
        return 1
    print(f"OK: scheduled exams check passed after waiting {waited:.0f} s; screenshots in {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
