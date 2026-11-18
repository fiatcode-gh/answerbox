# /// script
# requires-python = ">=3.12"
# dependencies = ["playwright==1.63.0"]
# ///
"""Browser check for archiving exams in real Chromium: Archive closes an open
exam with attempts and keeps it on its page with the date, the exams list hides
it and says when every exam is archived, Show archived lists it with results,
History and Download still working, a student in progress finishes, a new start
is refused like an unknown code, Open and Save schedule from pages opened
before the archive are refused with 409, Unarchive brings the exam back closed,
and a second exam stays archived. Needs a fresh instance.

With --rollback it checks, against the previous binary (5716eb8) on a copy of
the data directory a normal run left, that students, results and the editor
still work there, that the archived exam shows as closed, and that its Open and
Save schedule answer 500 and change nothing.

uv run e2e/archive.py --base-url URL --admin-email EMAIL --oidc-url URL --out DIR [--rollback]
"""

import argparse
import re
import sys
import tempfile
from contextlib import contextmanager
from datetime import datetime, timedelta
from pathlib import Path
from zoneinfo import ZoneInfo

from playwright.sync_api import Locator, Page, expect, sync_playwright

from common import (
    REFUSED,
    add_sign_in_args,
    build_fixture_zip,
    exam_code,
    foreign_requests,
    import_exam,
    open_exam,
    require_cross_site,
    start,
    teacher_login,
    watch,
)

DESKTOP = {"viewport": {"width": 1280, "height": 800}}
PHONE_VIEWPORT = {"width": 412, "height": 915}
TITLE_A = "Archive check"
TITLE_B = "Old quiz"
TITLE_NEW = "Rollback check"
AYU, BUDI, CITRA, DEWI, EKA = "Ayu Lestari", "Budi Santoso", "Citra Dewi", "Dewi Anggraini", "Eka Putra"
ARCHIVED_ALERT = "This exam is archived. Unarchive it to open or schedule it."
ARCHIVED_NOTE = re.compile(r"^Archived on \w{3}, \d{1,2} \w{3} \d{4}\.")
NO_SCHEDULE = "No schedule. The exam follows the Open and Close buttons."
INPUT_LAYOUT = "%Y-%m-%dT%H:%M"
SCORE = re.compile(r"^\d+ / \d+$")


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


def console_error(url: str, status: int, text: str) -> str:
    return f"{url}: Failed to load resource: the server responded with a status of {status} ({text})"


def check_logs(logs: list[dict], base_url: str, oidc_url: str, expected: list[str]) -> None:
    """No page errors or foreign requests; the console errors are exactly expected, in order."""
    problems, errors = [], []
    for log in logs:
        errors += log["console_errors"]
        problems += [f"page error: {e}" for e in log["page_errors"]]
        problems += [f"foreign request: {u}" for u in foreign_requests(log["requests"], base_url, oidc_url)]
    if errors != expected:
        problems.append("console errors:\n    " + "\n    ".join(errors) + "\n  want:\n    " + "\n    ".join(expected))
    if problems:
        raise AssertionError("\n  ".join(["problems found:"] + problems))


def exam_page(page: Page, base_url: str, exam_id: int) -> None:
    page.goto(f"{base_url}/admin/exams/{exam_id}")
    expect(page.locator("main.exam-detail")).to_be_visible()


def exams_list(page: Page, base_url: str, archived: bool = False) -> None:
    page.goto(f"{base_url}/admin/exams" + ("?archived=1" if archived else ""))
    expect(page.locator("main h1")).to_have_text("Exams")


def row(page: Page, exam_id: int) -> Locator:
    return page.locator(f"tr[data-exam-id='{exam_id}']")


def row_by_title(page: Page, title: str) -> Locator:
    return page.locator("#exams tbody tr[data-exam-id]").filter(has=page.get_by_role("cell", name=title, exact=True))


def head_badge(page: Page) -> Locator:
    return page.locator(".exam-head .badge")


def action_form(scope: Page | Locator, exam_id: int, action: str) -> Locator:
    return scope.locator(f"form[action='/admin/exams/{exam_id}/{action}']")


def press(page: Page, selector: str, url: str) -> int:
    """Click the submit button of the form at selector; returns the status of the POST to url."""
    with page.expect_response(lambda r: r.url == url and r.request.method == "POST") as info:
        page.locator(f"{selector} button[type=submit]").click()
    page.wait_for_load_state()
    return info.value.status


def fill_open_time_ahead(page: Page) -> None:
    """Type an open time one hour ahead, in the zone the page names, into #schedule-opens."""
    note = page.locator("section.schedule .zone-note").inner_text()
    zone = re.search(r"Times are shown in (\S+?)\.", note)
    if not zone:
        raise AssertionError(f"no time zone in {note!r}")
    opens = datetime.now(ZoneInfo(zone.group(1))) + timedelta(hours=1)
    page.locator("#schedule-opens").fill(opens.strftime(INPUT_LAYOUT))


def answer_one(page: Page) -> None:
    """Pick the first option of the first question and wait until it is saved."""
    section = page.locator("section.question").first
    section.locator("label.option").first.click()
    expect(section.locator(".q-state")).to_have_attribute("data-state", "saved")
    expect(page.locator("#save-state")).to_have_attribute("data-state", "saved")


def submit(page: Page) -> str:
    """Submit the attempt on the exam page; returns the score on the result page."""
    page.locator("#submit-open").click()
    expect(page.locator("#submit-dialog")).to_be_visible()
    page.locator("#submit-confirm").click()
    expect(page.locator("main#result")).to_be_visible()
    score = page.locator("#score").inner_text().strip()
    if not SCORE.match(score):
        raise AssertionError(f"result page shows score {score!r}")
    return score


def take(page: Page, base_url: str, code: str, name: str) -> str:
    """Start with code, answer one question, submit; returns the score."""
    start(page, base_url, code, name)
    answer_one(page)
    return submit(page)


def refused_start(page: Page, base_url: str, code: str, name: str) -> None:
    """Type code and name and press Start: refused on the landing page with the name kept."""
    page.goto(base_url + "/")
    form = page.locator("#start-form")
    form.locator("input[name=code]").fill(code)
    form.locator("input[name=name]").fill(name)
    form.locator("button[type=submit]").click()
    expect(page.locator("[role=alert]")).to_have_text(REFUSED)
    expect(page.locator("main#landing")).to_be_visible()
    expect(page.locator("#start-form input[name=name]")).to_have_value(name)
    expect(page.locator("main#exam")).to_have_count(0)


def landing_html(page: Page, base_url: str, code: str) -> str:
    response = page.goto(f"{base_url}/?code={code}")
    if response.status != 200:
        raise AssertionError(f"/?code={code} answered {response.status}, want 200")
    expect(page.locator("[role=alert]")).to_have_text(REFUSED)
    return page.locator("main#landing").inner_html()


def unknown_code(*codes: str) -> str:
    return next(c for c in ("ZZZZZZ", "YYYYYY", "XXXXXX") if c not in codes)


def attempt_rows(page: Page, base_url: str, exam_id: int) -> Locator:
    page.goto(f"{base_url}/admin/exams/{exam_id}/attempts")
    return page.locator("#attempts tbody tr[data-attempt-id]")


def attempt_row(page: Page, base_url: str, exam_id: int, name: str) -> Locator:
    return attempt_rows(page, base_url, exam_id).filter(has=page.get_by_role("cell", name=name, exact=True))


def open_attempt(page: Page, base_url: str, attempt: Locator, name: str) -> None:
    """Follow the attempt row's View link: 200 with the student's name."""
    href = attempt.get_by_role("link", name="View", exact=True).get_attribute("href")
    response = page.goto(base_url + href)
    if response.status != 200:
        raise AssertionError(f"{href} answered {response.status}, want 200")
    expect(page.locator("main.attempt-detail h1")).to_have_text(name)


def run(base_url: str, admin_email: str, oidc_url: str, out: Path) -> None:
    out.mkdir(parents=True, exist_ok=True)
    logs = []
    with sync_playwright() as p, tempfile.TemporaryDirectory() as tmp:
        tmp = Path(tmp)
        browser = p.chromium.launch(headless=True)
        phone = p.devices["Pixel 7"]

        def new_context(**kwargs):
            context = browser.new_context(**kwargs)
            logs.append(watch(context, base_url))
            return context

        def shot(page: Page, name: str) -> None:
            page.screenshot(path=out / name, full_page=True, animations="disabled")

        a = new_context(**DESKTOP).new_page()
        with step("1 admin signs in; the instance is fresh"):
            teacher_login(a, base_url, admin_email, "Ada Admin")
            if a.get_by_text("No exams yet.").count() != 1:
                raise AssertionError("not a fresh instance: the exams page does not say 'No exams yet.'")

        with step("2 import and open Archive check; Ayu submits, Budi stays in progress"):
            (tmp / "a").mkdir()
            zip_a, _ = build_fixture_zip(tmp / "a", title=TITLE_A, monitor="off")
            exam_a = import_exam(a, zip_a)
            # An exam opened by the button shows no Open form, so the tab that
            # presses Open after the archive is opened while the exam is closed.
            stale_open = a.context.new_page()
            exam_page(stale_open, base_url, exam_a)
            expect(action_form(stale_open, exam_a, "open")).to_be_visible()
            open_exam(a, exam_a)
            code_a = exam_code(a, exam_a)
            ayu = new_context(**phone).new_page()
            ayu_score = take(ayu, base_url, code_a, AYU)
            budi = new_context(**phone).new_page()
            start(budi, base_url, code_a, BUDI)

        with step("3 another teacher tab keeps the open exam's page with its schedule form"):
            stale_schedule = a.context.new_page()
            exam_page(stale_schedule, base_url, exam_a)
            expect(head_badge(stale_schedule)).to_have_text("Open")
            expect(stale_schedule.locator("#schedule-form")).to_be_visible()

        with step("4 Archive: the exam page shows Archived with the date and no Open, Close or schedule"):
            exam_page(a, base_url, exam_a)
            a.locator("#archive-form button[type=submit]").click()
            expect(a).to_have_url(re.compile(rf"/admin/exams/{exam_a}$"))
            expect(head_badge(a)).to_have_text("Archived")
            expect(a.locator("#archived-note")).to_have_text(ARCHIVED_NOTE)
            for action in ("open", "close", "schedule"):
                expect(action_form(a, exam_a, action)).to_have_count(0)
            expect(a.locator("#schedule-form")).to_have_count(0)
            expect(a.locator("#archive-form")).to_have_count(0)
            expect(a.locator("#unarchive-form")).to_be_visible()
            expect(a.locator(f"a[href='/admin/exams/{exam_a}/attempts']")).to_be_visible()
            expect(a.locator("#history-link")).to_be_visible()
            expect(a.locator("#download-link")).to_be_visible()
            expect(a.locator("#copy-form")).to_be_visible()
            shot(a, "desktop-archived-exam.png")
            a.set_viewport_size(PHONE_VIEWPORT)
            no_page_overflow(a)
            shot(a, "phone-archived-exam.png")
            a.set_viewport_size(DESKTOP["viewport"])

        with step("5 the exams list says every exam is archived; Show archived lists it, Hide archived returns"):
            exams_list(a, base_url)
            expect(row(a, exam_a)).to_have_count(0)
            expect(a.locator("#exams")).to_have_count(0)
            expect(a.locator("#all-archived")).to_have_text(re.compile(r"^All exams are archived\."))
            expect(a.locator("#all-archived a#show-archived")).to_have_text("Show archived")
            shot(a, "desktop-all-archived.png")
            a.locator("#show-archived").click()
            expect(a).to_have_url(re.compile(r"/admin/exams\?archived=1$"))
            archived = row(a, exam_a)
            expect(archived.locator(".badge")).to_have_text("Archived")
            expect(action_form(archived, exam_a, "open")).to_have_count(0)
            expect(action_form(archived, exam_a, "close")).to_have_count(0)
            expect(archived.get_by_role("link", name="View", exact=True)).to_be_visible()
            expect(archived.get_by_role("link", name="Results", exact=True)).to_be_visible()
            expect(a.locator("#hide-archived")).to_be_visible()
            shot(a, "desktop-show-archived.png")
            a.locator("#hide-archived").click()
            expect(a).to_have_url(re.compile(r"/admin/exams$"))
            expect(a.locator("#all-archived")).to_be_visible()

        with step("6 results, the attempt page, History and Download work on the archived exam"):
            exams_list(a, base_url, archived=True)
            row(a, exam_a).get_by_role("link", name="Results", exact=True).click()
            expect(a).to_have_url(re.compile(rf"/admin/exams/{exam_a}/attempts$"))
            rows = a.locator("#attempts tbody tr[data-attempt-id]")
            expect(rows).to_have_count(2)
            ayu_row = rows.filter(has=a.get_by_role("cell", name=AYU, exact=True))
            expect(ayu_row.locator(".badge")).to_have_text("Submitted")
            expect(ayu_row).to_contain_text(ayu_score)
            expect(rows.filter(has=a.get_by_role("cell", name=BUDI, exact=True)).locator(".badge")).to_have_text("In progress")
            open_attempt(a, base_url, ayu_row, AYU)
            exam_page(a, base_url, exam_a)
            a.locator("#history-link").click()
            expect(a).to_have_url(re.compile(rf"/admin/exams/{exam_a}/revisions$"))
            revisions = a.locator("#revisions tbody tr[data-revision]")
            expect(revisions).to_have_count(1)
            cells = [c.strip() for c in revisions.first.locator("td").all_inner_texts()]
            if cells[0] != "1" or cells[3] != "Imported":
                raise AssertionError(f"history row {cells}, want revision 1 Imported")
            exam_page(a, base_url, exam_a)
            with a.expect_download() as info:
                a.locator("#download-link").click()
            if info.value.suggested_filename != f"exam-{code_a}.zip":
                raise AssertionError(f"download named {info.value.suggested_filename!r}, want exam-{code_a}.zip")

        with step("7 Budi, in progress, answers and submits; the results list shows him submitted"):
            answer_one(budi)
            budi_score = submit(budi)
            budi_row = attempt_row(a, base_url, exam_a, BUDI)
            expect(budi_row.locator(".badge")).to_have_text("Submitted")
            expect(budi_row).to_contain_text(budi_score)

        dewi = new_context(**phone).new_page()
        with step("8 a new start with the archived code is refused like an unknown code"):
            refused_start(dewi, base_url, code_a, DEWI)
            dewi.screenshot(path=out / "phone-refused.png", full_page=True, animations="disabled")
            archived_html = landing_html(dewi, base_url, code_a)
            unknown_html = landing_html(dewi, base_url, unknown_code(code_a))
            if archived_html != unknown_html:
                raise AssertionError(f"archived and unknown codes differ:\n{archived_html}\n---\n{unknown_html}")

        with step("9 Open and Save schedule from pages opened before the archive answer 409"):
            open_url = f"{base_url}/admin/exams/{exam_a}/open"
            stale_open.bring_to_front()
            if (status := press(stale_open, f"form[action='/admin/exams/{exam_a}/open']", open_url)) != 409:
                raise AssertionError(f"Open on the archived exam answered {status}, want 409")
            expect(stale_open.locator("[role=alert]")).to_have_text(ARCHIVED_ALERT)
            stale_schedule.bring_to_front()
            fill_open_time_ahead(stale_schedule)
            schedule_url = f"{base_url}/admin/exams/{exam_a}/schedule"
            if (status := press(stale_schedule, "#schedule-form", schedule_url)) != 409:
                raise AssertionError(f"Save schedule on the archived exam answered {status}, want 409")
            expect(stale_schedule.locator("[role=alert]")).to_have_text(ARCHIVED_ALERT)
            shot(stale_schedule, "desktop-refused-schedule.png")
            a.bring_to_front()
            exam_page(a, base_url, exam_a)
            expect(head_badge(a)).to_have_text("Archived")

        with step("10 Unarchive: the exam is back closed with Open, schedule and Archive"):
            a.locator("#unarchive-form button[type=submit]").click()
            expect(a).to_have_url(re.compile(rf"/admin/exams/{exam_a}$"))
            expect(head_badge(a)).to_have_text("Closed")
            expect(action_form(a, exam_a, "open")).to_be_visible()
            expect(a.locator("#schedule-form")).to_be_visible()
            expect(a.locator("#schedule-times")).to_have_text(NO_SCHEDULE)
            expect(a.locator("#archive-form")).to_be_visible()
            expect(a.locator("#archived-note")).to_have_count(0)
            exams_list(a, base_url)
            expect(row(a, exam_a).locator(".badge")).to_have_text("Closed")
            expect(a.locator("#show-archived")).to_have_count(0)
            expect(a.locator("#hide-archived")).to_have_count(0)

        with step("11 Open again: Dewi starts with the code"):
            open_exam(a, exam_a)
            start(dewi, base_url, code_a, DEWI)
            expect(dewi.locator(".exam-title")).to_have_text(TITLE_A)

        with step("12 Old quiz: Citra submits, it is archived; the lists show one and both exams"):
            exams_list(a, base_url)
            (tmp / "b").mkdir()
            zip_b, _ = build_fixture_zip(tmp / "b", title=TITLE_B, monitor="off")
            exam_b = import_exam(a, zip_b)
            open_exam(a, exam_b)
            code_b = exam_code(a, exam_b)
            citra = new_context(**phone).new_page()
            take(citra, base_url, code_b, CITRA)
            exam_page(a, base_url, exam_b)
            a.locator("#archive-form button[type=submit]").click()
            expect(a).to_have_url(re.compile(rf"/admin/exams/{exam_b}$"))
            expect(head_badge(a)).to_have_text("Archived")
            exams_list(a, base_url)
            expect(a.locator("#exams tbody tr[data-exam-id]")).to_have_count(1)
            expect(row(a, exam_a).locator(".badge")).to_have_text("Open")
            expect(a.locator("#show-archived")).to_be_visible()
            exams_list(a, base_url, archived=True)
            expect(a.locator("#exams tbody tr[data-exam-id]")).to_have_count(2)
            expect(row(a, exam_a).locator(".badge")).to_have_text("Open")
            expect(row(a, exam_b).locator(".badge")).to_have_text("Archived")
            shot(a, "desktop-final-list.png")

        with step("13 no page errors or foreign requests; console errors are the two 409s and the refused start"):
            check_logs(
                logs,
                base_url,
                oidc_url,
                [
                    console_error(f"{base_url}/admin/exams/{exam_a}/open", 409, "Conflict"),
                    console_error(f"{base_url}/admin/exams/{exam_a}/schedule", 409, "Conflict"),
                    console_error(f"{base_url}/start", 422, "Unprocessable Entity"),
                ],
            )

        browser.close()


def run_rollback(base_url: str, admin_email: str, oidc_url: str, out: Path) -> None:
    out.mkdir(parents=True, exist_ok=True)
    logs = []
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        phone = p.devices["Pixel 7"]

        def new_context(**kwargs):
            context = browser.new_context(**kwargs)
            logs.append(watch(context, base_url))
            return context

        a = new_context(**DESKTOP).new_page()
        with step("R1 admin signs in"):
            teacher_login(a, base_url, admin_email, "Ada Admin")

        with step("R2 the list has Archive check open and Old quiz closed with an Open button"):
            row_a, row_b = row_by_title(a, TITLE_A), row_by_title(a, TITLE_B)
            expect(row_a).to_have_count(1)
            expect(row_b).to_have_count(1)
            expect(row_a.locator(".badge")).to_have_text("Open")
            expect(row_b.locator(".badge")).to_have_text("Closed")
            exam_a = int(row_a.get_attribute("data-exam-id"))
            exam_b = int(row_b.get_attribute("data-exam-id"))
            expect(action_form(row_b, exam_b, "open")).to_be_visible()
            code_a, code_b = exam_code(a, exam_a), exam_code(a, exam_b)
            a.screenshot(path=out / "desktop-exams.png", full_page=True, animations="disabled")

        with step("R3 results: Citra submitted on Old quiz with her attempt page; Ayu, Budi and Dewi on Archive check"):
            citra_row = attempt_row(a, base_url, exam_b, CITRA)
            expect(citra_row.locator(".badge")).to_have_text("Submitted")
            expect(citra_row.locator("td.numeric").nth(2)).to_have_text(SCORE)
            open_attempt(a, base_url, citra_row, CITRA)
            names = attempt_rows(a, base_url, exam_a).locator("td:first-child").all_inner_texts()
            if sorted(n.strip() for n in names) != sorted([AYU, BUDI, DEWI]):
                raise AssertionError(f"Archive check has attempts {names}, want {[AYU, BUDI, DEWI]}")

        with step("R4 Eka takes Archive check; the results list has her"):
            eka = new_context(**phone).new_page()
            eka_score = take(eka, base_url, code_a, EKA)
            eka_row = attempt_row(a, base_url, exam_a, EKA)
            expect(eka_row.locator(".badge")).to_have_text("Submitted")
            expect(eka_row).to_contain_text(eka_score)

        with step("R5 a start with Old quiz's code is refused"):
            refused = new_context(**phone).new_page()
            refused_start(refused, base_url, code_b, EKA)

        with step("R6 Open on Old quiz answers 500 and changes nothing"):
            exams_list(a, base_url)
            open_url = f"{base_url}/admin/exams/{exam_b}/open"
            if (status := press(a, f"form[action='/admin/exams/{exam_b}/open']", open_url)) != 500:
                raise AssertionError(f"Open on the archived exam answered {status}, want 500")
            a.screenshot(path=out / "desktop-open-500.png", full_page=True, animations="disabled")
            exams_list(a, base_url)
            expect(row(a, exam_b).locator(".badge")).to_have_text("Closed")
            landing_html(refused, base_url, code_b)

        with step("R7 Save schedule on Old quiz answers 500 and changes nothing"):
            exam_page(a, base_url, exam_b)
            fill_open_time_ahead(a)
            schedule_url = f"{base_url}/admin/exams/{exam_b}/schedule"
            if (status := press(a, "#schedule-form", schedule_url)) != 500:
                raise AssertionError(f"Save schedule on the archived exam answered {status}, want 500")
            exam_page(a, base_url, exam_b)
            expect(a.locator("#schedule-times")).to_have_text(NO_SCHEDULE)
            expect(head_badge(a)).to_have_text("Closed")

        with step("R8 the editor still works: New exam lands on its editor page"):
            exams_list(a, base_url)
            a.locator("#new-exam").click()
            expect(a).to_have_url(re.compile(r"/admin/exams/new$"))
            form = a.locator("#new-exam-form")
            form.locator("#exam-title").fill(TITLE_NEW)
            form.locator("#question-text").fill("Which number is even?")
            boxes = form.locator("textarea[name=option]")
            boxes.nth(0).fill("4")
            boxes.nth(1).fill("5")
            while boxes.count() > 2:
                form.locator(".option-row").nth(boxes.count() - 1).get_by_role("button", name="Remove", exact=True).click()
                expect(a).to_have_url(re.compile(r"/admin/exams/new$"))
            form.locator(".option-row").nth(0).locator("input[type=radio][name=correct]").check()
            form.locator("#exam-title").press("Enter")
            expect(a).to_have_url(re.compile(r"/admin/exams/\d+/edit$"))
            expect(a.locator("section.preview-question")).to_have_count(1)

        with step("R9 no page errors or foreign requests; console errors are the two 500s and the refused start"):
            check_logs(
                logs,
                base_url,
                oidc_url,
                [
                    console_error(f"{base_url}/admin/exams/{exam_b}/open", 500, "Internal Server Error"),
                    console_error(f"{base_url}/admin/exams/{exam_b}/schedule", 500, "Internal Server Error"),
                    console_error(f"{base_url}/start", 422, "Unprocessable Entity"),
                ],
            )

        browser.close()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", required=True)
    add_sign_in_args(parser)
    parser.add_argument("--out", type=Path, default=Path(__file__).parent / "out" / "archive")
    parser.add_argument("--rollback", action="store_true", help="check the previous binary on the data a normal run left")
    args = parser.parse_args()
    base_url, oidc_url = args.base_url.rstrip("/"), args.oidc_url.rstrip("/")
    require_cross_site(base_url, oidc_url)
    try:
        (run_rollback if args.rollback else run)(base_url, args.admin_email, oidc_url, args.out)
    except StepFailed as err:
        print(err, file=sys.stderr)
        return 1
    mode = "rollback check" if args.rollback else "archive check"
    print(f"OK: {mode} passed; screenshots in {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
