# /// script
# requires-python = ">=3.12"
# dependencies = ["playwright==1.63.0"]
# ///
"""Walk-through of Answerbox in real Chromium: every teacher page with scripts
disabled, the student exam and the Open, Copy link and Close controls by
keyboard only, the results list that stops refreshing by itself, a 360 px
phone, and the dark and light colour schemes. Needs a fresh instance.

uv run e2e/walkthrough.py --base-url URL --admin-email EMAIL --oidc-url URL --out DIR
"""

import argparse
import csv
import re
import sys
import tempfile
from contextlib import contextmanager
from datetime import datetime, timedelta
from pathlib import Path
from urllib.parse import urlsplit
from zoneinfo import ZoneInfo

from playwright.sync_api import Page, expect, sync_playwright

from common import (
    add_sign_in_args,
    build_fixture_zip,
    confirm,
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
PHONE = {"viewport": {"width": 360, "height": 640}}
INPUT_LAYOUT = "%Y-%m-%dT%H:%M"
SHOWN_LAYOUT = "%Y-%m-%d %H:%M"
WRITTEN = "Written walk-through"
IMPORTED = "Imported walk-through"
STUDENT = "Sari Student"
RENAMED = "Sari Renamed"
ADDED_TEACHER = "walk@example.test"
TOUCH = 44
# Computed body colours of the two schemes: (background, text).
SCHEMES = {"dark": ("rgb(20, 23, 28)", "rgb(231, 233, 238)"), "light": ("rgb(250, 248, 243)", "rgb(27, 35, 48)")}


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


def exam_row(page: Page, exam_id: int):
    return page.locator(f"#exams tbody tr[data-exam-id='{exam_id}']")


def state_button(page: Page, exam_id: int, action: str):
    """The Open (action "open") or Close ("close") button of the exam page or of a list row."""
    return page.locator(f"form[action='/admin/exams/{exam_id}/{action}'] button[type=submit]")


def tab_to(page: Page, selector: str, limit: int = 200) -> None:
    """Press Tab until the focused element matches selector."""
    for _ in range(limit):
        page.keyboard.press("Tab")
        if page.evaluate("s => !!document.activeElement && document.activeElement.matches(s)", selector):
            return
    raise AssertionError(f"{limit} presses of Tab never focused {selector} on {page.url}")


def ring(page: Page) -> None:
    """The focused element (for a radio: its label.option) shows an outline at least 2 px wide."""
    style, width, what = page.evaluate(
        """() => {
            let el = document.activeElement;
            if (el.matches("input[type=radio]") && el.closest("label.option")) el = el.closest("label.option");
            const s = getComputedStyle(el);
            return [s.outlineStyle, parseFloat(s.outlineWidth), el.outerHTML.slice(0, 80)];
        }"""
    )
    if style == "none" or width < 2:
        raise AssertionError(f"no focus ring on {what}: outline {style} {width}px")


def box(locator):
    b = locator.bounding_box()
    if b is None:
        raise AssertionError(f"{locator} is not rendered")
    return b


def at_least(locator, height: float, what: str) -> None:
    if (h := box(locator)["height"]) < height:
        raise AssertionError(f"{what} is {h:.1f} px high, want at least {height}")


def ends_inside(locator, width: float, what: str) -> None:
    b = box(locator)
    if b["x"] < 0 or b["x"] + b["width"] > width:
        raise AssertionError(f"{what} spans x {b['x']:.1f} to {b['x'] + b['width']:.1f}, want inside 0 to {width}")


def in_window(locator, viewport: dict, what: str) -> None:
    b = box(locator)
    if b["x"] < 0 or b["y"] < 0 or b["x"] + b["width"] > viewport["width"] or b["y"] + b["height"] > viewport["height"]:
        raise AssertionError(f"{what} at {b}, want inside the {viewport['width']} × {viewport['height']} window")


def centred(locator):
    """Scroll locator to the middle of the window: wide editor forms keep their
    save bar stuck to the bottom edge, where Playwright's own scroll leaves it."""
    locator.evaluate("el => el.scrollIntoView({block: 'center'})")
    return locator


def fill_options(form, prefix: str) -> None:
    """Give every option row of the question form a text: an empty one is refused."""
    boxes = form.locator("textarea[name=option]")
    for i in range(boxes.count()):
        boxes.nth(i).fill(f"{prefix} {chr(ord('A') + i)}")


def answer_first(page: Page) -> None:
    """Pick the first option of the first question on screen and wait until it is saved."""
    question = page.locator("section.question").first
    question.locator("label.option").first.click()
    expect(question.locator(".q-state")).to_have_attribute("data-state", "saved")
    expect(page.locator("#save-state")).to_have_attribute("data-state", "saved")


def submit(page: Page) -> None:
    page.locator("#submit-open").click()
    page.locator("#submit-confirm").click()
    expect(page.locator("main#result")).to_be_visible()


def attempt_row(page: Page, name: str):
    return page.locator("#attempts tbody tr").filter(has_text=name)


def run(base_url: str, admin_email: str, oidc_url: str, out: Path) -> None:
    out.mkdir(parents=True, exist_ok=True)
    logs = []
    with sync_playwright() as p, tempfile.TemporaryDirectory() as tmp:
        browser = p.chromium.launch(headless=True)

        def new_context(**kwargs):
            context = browser.new_context(**kwargs)
            logs.append(watch(context, base_url))
            return context

        def shot(page: Page, name: str) -> None:
            page.screenshot(path=out / name, full_page=True, animations="disabled")

        def fixture(title: str):
            (Path(tmp) / title).mkdir()
            return build_fixture_zip(Path(tmp) / title, title=title)[0]

        # A. Every teacher page works with scripts disabled.
        t = new_context(java_script_enabled=False, **DESKTOP).new_page()
        with step("1 / shows the start form and What is Answerbox?; Teacher sign-in leads through the provider to the exams list"):
            t.goto(base_url + "/")
            expect(t.locator("#start-form")).to_be_visible()
            expect(t.locator("section.about h2")).to_have_text("What is Answerbox?")
            shot(t, "a-landing-noscript.png")
            t.get_by_role("link", name="Teacher sign-in").click()
            expect(t).to_have_url(re.compile(r"/admin/login$"))
            t.locator("#google-sign-in").click()
            form = t.locator("#fake-signin")
            form.locator("input[name=email]").fill(admin_email)
            form.locator("input[name=name]").fill("Walk Admin")
            form.locator("button[type=submit]").click()
            expect(t).to_have_url(re.compile(r"/admin/exams$"))
            expect(t.locator("#signed-in-as")).to_have_text(admin_email)

        with step("2 New exam: Add option returns the form with one more row; save lands on the editor"):
            empty = t.locator("p.empty")
            expect(empty).to_contain_text("New exam")
            expect(empty).to_contain_text("Import from file")
            t.locator("#new-exam").click()
            expect(t).to_have_url(re.compile(r"/admin/exams/new$"))
            form = t.locator("#new-exam-form")
            form.locator("#exam-title").fill(WRITTEN)
            form.locator("#exam-shuffle").select_option("none")
            form.locator("#exam-duration").fill("20")
            form.locator("#question-text").fill("Question one?")
            rows = form.locator("textarea[name=option]").count()
            fill_options(form, "One")
            centred(form.get_by_role("button", name="Add option", exact=True)).click()
            expect(t).to_have_url(re.compile(r"/admin/exams/new$"))
            expect(form.locator("textarea[name=option]")).to_have_count(rows + 1)
            expect(form.locator("#exam-title")).to_have_value(WRITTEN)
            expect(form.locator("#question-text")).to_have_value("Question one?")
            fill_options(form, "One")
            centred(form.locator(".option-row").nth(0).locator("input[name=correct]")).check()
            shot(t, "a-new-exam-noscript.png")
            form.locator("#save").click()
            expect(t).to_have_url(re.compile(r"/admin/exams/\d+/edit$"))
            written_id = int(re.search(r"/admin/exams/(\d+)/edit$", t.url).group(1))
            expect(t.locator("section.preview-question")).to_have_count(1)

        def editor_landing() -> None:
            expect(t).to_have_url(re.compile(rf"/admin/exams/{written_id}/edit(#.*)?$"))
            expect(t.locator("main.editor-page")).to_be_visible()

        with step("3 editor: Add question twice, Move down, Settings, Start a section, Edit section"):
            for n in (2, 3):
                t.locator("#add-question").click()
                expect(t).to_have_url(re.compile(rf"/admin/exams/{written_id}/questions/new$"))
                form = t.locator("#question-form")
                form.locator("#question-text").fill(f"Question {n}?")
                fill_options(form, f"Q{n}")
                centred(form.locator(".option-row").nth(1).locator("input[name=correct]")).check()
                form.locator("#save").click()
                editor_landing()
                expect(t.locator("section.preview-question")).to_have_count(n)
            t.locator("#q1 form.move-down button[type=submit]").click()
            editor_landing()
            expect(t.locator("#q1 .question-body")).to_contain_text("Question 2?")
            expect(t.locator("#q2 .question-body")).to_contain_text("Question one?")
            t.locator("#settings-link").click()
            t.locator("#exam-duration").fill("25")
            t.locator("#settings-form button[type=submit]").click()
            editor_landing()
            t.locator("#q1 a.start-section").click()
            t.locator("#section-title").fill("Part A")
            t.locator("#section-form #save").click()
            editor_landing()
            expect(t.locator("#s1 .section-title")).to_have_text("Part A")
            t.locator("#s1 a.edit-section").click()
            t.locator("#section-title").fill("Part One")
            t.locator("#section-form #save").click()
            editor_landing()
            expect(t.locator("#s1 .section-title")).to_have_text("Part One")
            t.goto(f"{base_url}/admin/exams/{written_id}")
            expect(t.locator("dl.settings dd").first).to_have_text("25 minutes")
            t.goto(f"{base_url}/admin/exams/{written_id}/edit")
            shot(t, "a-editor-noscript.png")

        with step("4 Remove question and Remove section go through their confirmation pages; Cancel keeps them"):
            t.locator("#q3 a.remove-question").click()
            expect(t.locator("#remove-question-form")).to_be_visible()
            shot(t, "a-confirm-remove-question-noscript.png")
            t.locator("main#confirm").get_by_role("link", name="Cancel").click()
            editor_landing()
            expect(t.locator("section.preview-question")).to_have_count(3)
            t.locator("#q3 a.remove-question").click()
            confirm(t, "remove-question-form")
            editor_landing()
            expect(t.locator("section.preview-question")).to_have_count(2)
            t.locator("#s1 a.remove-section").click()
            expect(t.locator("#remove-section-form")).to_be_visible()
            t.locator("main#confirm").get_by_role("link", name="Cancel").click()
            editor_landing()
            expect(t.locator(".editor-section")).to_have_count(1)
            t.locator("#s1 a.remove-section").click()
            confirm(t, "remove-section-form")
            editor_landing()
            expect(t.locator(".editor-section")).to_have_count(0)
            expect(t.locator("section.preview-question")).to_have_count(2)

        with step("5 exams list: Add option stored nothing; import a file through section#import"):
            t.goto(f"{base_url}/admin/exams")
            expect(t.locator("#exams tbody tr")).to_have_count(1)
            t.locator("section#import #import-file").set_input_files(str(fixture(IMPORTED)))
            t.locator("section#import button[type=submit]").click()
            expect(t).to_have_url(re.compile(r"/admin/exams/\d+$"))
            exam_id = int(re.search(r"/admin/exams/(\d+)$", t.url).group(1))
            exam_page = re.compile(rf"/admin/exams/{exam_id}$")
            expect(t.locator(".exam-head h1")).to_have_text(IMPORTED)

        with step("6 exam page: no Copy link, a schedule is saved, Open exam lands back here and drops the schedule"):
            expect(t.locator("#copy-link")).to_be_hidden()
            expect(t.locator("#student-link")).to_be_visible()
            zone = ZoneInfo(re.search(r"Times are shown in (\S+?)\.\s", t.locator(".zone-note").inner_text()).group(1))
            opens = (datetime.now(zone) + timedelta(hours=1)).replace(second=0, microsecond=0)
            t.locator("#schedule-details > summary").click()
            t.locator("#schedule-opens").fill(opens.strftime(INPUT_LAYOUT))
            t.locator("#schedule-form button[type=submit]").click()
            expect(t).to_have_url(exam_page)
            expect(t.locator("#schedule-times")).to_contain_text(f"Opens {opens.strftime(SHOWN_LAYOUT)}")
            expect(state_button(t, exam_id, "open")).to_have_text("Open exam")
            shot(t, "a-exam-scheduled-noscript.png")
            state_button(t, exam_id, "open").click()
            expect(t).to_have_url(exam_page)
            expect(t.locator(".exam-head .badge")).to_have_text("Open")
            expect(state_button(t, exam_id, "open")).to_have_count(0)
            expect(state_button(t, exam_id, "close")).to_have_text("Close exam")
            expect(t.locator("#schedule-times")).to_contain_text("No schedule.")
            code = t.locator("#exam-code").inner_text().strip()
            shot(t, "a-exam-open-noscript.png")

        with step("7 Projector view shows the code; Back to the exam"):
            t.locator("#projector-link").click()
            expect(t.locator("#projector-code")).to_have_text(code)
            shot(t, "a-projector-noscript.png")
            t.get_by_role("link", name="Back to the exam").click()
            expect(t).to_have_url(exam_page)

        s = new_context(**DESKTOP).new_page()
        attempts_url = f"{base_url}/admin/exams/{exam_id}/attempts"
        with step("8 a student is in progress; Results without scripts: no live line, filter, sort, Export CSV"):
            start(s, base_url, code, STUDENT)
            answer_first(s)
            t.locator("nav.exam-links").get_by_role("link", name="Results").click()
            expect(t).to_have_url(attempts_url)
            expect(attempt_row(t, STUDENT)).to_contain_text("In progress")
            expect(t.locator("#results-status")).to_be_hidden()
            t.reload()
            expect(attempt_row(t, STUDENT)).to_contain_text("In progress")
            expect(t.locator("#results-status")).to_be_hidden()
            t.locator("#filter-name").fill("sari")
            t.locator("#name-filter button[type=submit]").click()
            expect(t).to_have_url(re.compile(r"[?&]name=sari"))
            expect(t.locator("#attempts tbody tr")).to_have_count(1)
            t.locator("a.sort-link", has_text="Score").click()
            expect(t).to_have_url(re.compile(r"sort=score"))
            expect(t).to_have_url(re.compile(r"name=sari"))
            expect(t.locator("th[aria-sort]")).to_contain_text("Score")
            t.locator("#name-filter button[type=submit]").click()
            expect(t).to_have_url(re.compile(r"sort=score"))
            expect(t.locator("th[aria-sort]")).to_contain_text("Score")
            shot(t, "a-results-noscript.png")
            with t.expect_download() as info:
                t.locator("#export-csv").click()
            csv_path = Path(tmp) / "results.csv"
            info.value.save_as(csv_path)
            with open(csv_path, newline="", encoding="utf-8-sig") as f:
                table = [r for r in csv.reader(f) if r]
            if len(table) != 2:
                raise AssertionError(f"the CSV has {len(table) - 1} data rows, want 1: {table}")

        with step("9 the student submits; View, Rename, back, Delete through its confirmation page"):
            submit(s)
            t.goto(attempts_url)
            expect(attempt_row(t, STUDENT)).to_contain_text("Submitted")
            attempt_row(t, STUDENT).get_by_role("link", name="View").click()
            expect(t.locator("main.attempt-detail h1")).to_have_text(STUDENT)
            t.locator("#rename-name").fill(RENAMED)
            t.locator("#rename-form button[type=submit]").click()
            expect(t.locator("main.attempt-detail h1")).to_have_text(RENAMED)
            shot(t, "a-attempt-noscript.png")
            t.locator("p.back a").click()
            expect(t).to_have_url(attempts_url)
            attempt_row(t, RENAMED).get_by_role("link", name="Delete").click()
            expect(t.locator("#delete-attempt-form")).to_be_visible()
            confirm(t, "delete-attempt-form")
            expect(t).to_have_url(attempts_url)
            expect(t.get_by_text("No attempts yet.")).to_have_count(1)

        with step("10 Teachers: add a teacher, Remove through its confirmation page"):
            t.locator("header").get_by_role("link", name="Teachers").click()
            t.locator("#add-teacher-email").fill(ADDED_TEACHER)
            t.locator("#add-teacher-form button[type=submit]").click()
            row = t.locator("#teachers tbody tr").filter(has_text=ADDED_TEACHER)
            expect(row).to_have_count(1)
            row.get_by_role("link", name="Remove").click()
            confirm(t, "remove-teacher-form")
            expect(t).to_have_url(re.compile(r"/admin/teachers$"))
            expect(t.locator("#teachers tbody tr").filter(has_text=ADDED_TEACHER)).to_have_count(0)

        with step("11 Close exam lands back on the exam page; Open and Close in a row land on the list; Archive and Unarchive"):
            t.goto(f"{base_url}/admin/exams/{exam_id}")
            state_button(t, exam_id, "close").click()
            expect(t).to_have_url(exam_page)
            expect(t.locator(".exam-head .badge")).to_have_text("Closed")
            t.goto(f"{base_url}/admin/exams")
            state_button(t, exam_id, "open").click()
            expect(t).to_have_url(re.compile(r"/admin/exams$"))
            expect(exam_row(t, exam_id).locator(".badge")).to_have_text("Open")
            state_button(t, exam_id, "close").click()
            expect(t).to_have_url(re.compile(r"/admin/exams$"))
            expect(exam_row(t, exam_id).locator(".badge")).to_have_text("Closed")
            shot(t, "a-exams-noscript.png")
            t.goto(f"{base_url}/admin/exams/{exam_id}")
            t.locator("#archive-link").click()
            expect(t.locator("#archive-form")).to_be_visible()
            shot(t, "a-confirm-archive-noscript.png")
            confirm(t, "archive-form")
            expect(t).to_have_url(exam_page)
            expect(t.locator(".exam-head .badge")).to_have_text("Archived")
            expect(t.locator("#archived-note")).to_be_visible()
            t.locator("#unarchive-form button[type=submit]").click()
            expect(t).to_have_url(exam_page)
            expect(t.locator(".exam-head .badge")).to_have_text("Closed")

        with step("12 History and a revision; Download; Copy exam lands on a new exam page"):
            t.locator("#history-link").click()
            expect(t.locator("#revisions tbody tr")).not_to_have_count(0)
            t.locator("#revisions tbody tr").first.get_by_role("link", name="View").click()
            expect(t.locator("main.revision-page")).to_be_visible()
            t.goto(f"{base_url}/admin/exams/{exam_id}")
            with t.expect_download() as info:
                t.locator("#download-link").click()
            if not info.value.suggested_filename.endswith(".zip"):
                raise AssertionError(f"Download gave {info.value.suggested_filename!r}, want a .zip")
            t.locator("#copy-form button[type=submit]").click()
            expect(t).to_have_url(re.compile(r"/admin/exams/\d+$"))
            copy_id = int(re.search(r"/admin/exams/(\d+)$", t.url).group(1))
            if copy_id == exam_id:
                raise AssertionError("Copy exam stayed on the same exam")
            expect(t.locator(".exam-head h1")).to_contain_text(IMPORTED)

        with step("13 Log out lands on the sign-in page"):
            t.locator("form[action='/admin/logout'] button[type=submit]").click()
            expect(t).to_have_url(re.compile(r"/admin/login$"))
            expect(t.locator("#google-sign-in")).to_be_visible()

        k = new_context(permissions=["clipboard-read", "clipboard-write"], **DESKTOP).new_page()
        with step("13a setup for B to D: a teacher with scripts imports three exams and opens two"):
            teacher_login(k, base_url, admin_email, "Key Admin")
            live_id = import_exam(k, fixture("Keyboard live"))
            open_exam(k, live_id)
            live_code = exam_code(k, live_id)
            k.goto(f"{base_url}/admin/exams")
            closed_id = import_exam(k, fixture("Keyboard closed"))
            k.goto(f"{base_url}/admin/exams")
            phone_id = import_exam(k, fixture("Phone check"))
            open_exam(k, phone_id)
            phone_code = exam_code(k, phone_id)

        # B. Keyboard only: keyboard.type and keyboard.press, goto between pages.
        ks = new_context(**DESKTOP).new_page()
        with step("14 student by keyboard: code, name, Enter; Space picks, ArrowDown moves the pick; submit"):
            ks.goto(base_url + "/")
            ks.wait_for_function("() => document.activeElement && document.activeElement.id === 'start-code'")
            ks.keyboard.type(live_code)
            ks.keyboard.press("Tab")
            ks.keyboard.type("Kiki Keys")
            ks.keyboard.press("Enter")
            expect(ks.locator("main#exam")).to_be_visible()
            tab_to(ks, "#q1 input[type=radio]")
            ks.keyboard.press("Space")
            ring(ks)
            first = ks.locator("#q1 input[type=radio]")
            expect(first.nth(0)).to_be_checked()
            ks.keyboard.press("ArrowDown")
            expect(first.nth(1)).to_be_checked()
            expect(first.nth(0)).not_to_be_checked()
            tab_to(ks, "#q2 input[type=radio]")
            ks.keyboard.press("Space")
            expect(ks.locator("#q2 input[type=radio]:checked")).to_have_count(1)
            expect(ks.locator("#save-state")).to_have_attribute("data-state", "saved")
            tab_to(ks, "#submit-open")
            ring(ks)
            shot(ks, "b-exam-keyboard.png")
            ks.keyboard.press("Enter")
            expect(ks.locator("#submit-dialog")).to_be_visible()
            tab_to(ks, "#submit-confirm")
            ks.keyboard.press("Enter")
            expect(ks.locator("main#result")).to_be_visible()
            shot(ks, "b-result-keyboard.png")

        with step("15 teacher by keyboard: Open exam lands back here, Copy link, Close exam lands back here"):
            closed_page = re.compile(rf"/admin/exams/{closed_id}$")
            k.goto(f"{base_url}/admin/exams/{closed_id}")
            expect(k.locator(".exam-head .badge")).to_have_text("Closed")
            tab_to(k, f"form[action='/admin/exams/{closed_id}/open'] button")
            ring(k)
            k.keyboard.press("Enter")
            expect(k.locator(".exam-head .badge")).to_have_text("Open")
            expect(k).to_have_url(closed_page)
            tab_to(k, "#copy-link")
            ring(k)
            k.keyboard.press("Enter")
            expect(k.locator("#copy-status")).to_have_text("Copied")
            link = k.locator("#student-link").get_attribute("href")
            if (copied := k.evaluate("navigator.clipboard.readText()")) != link:
                raise AssertionError(f"clipboard holds {copied!r}, want {link!r}")
            shot(k, "b-exam-copied-keyboard.png")
            tab_to(k, f"form[action='/admin/exams/{closed_id}/close'] button")
            k.keyboard.press("Enter")
            expect(k.locator(".exam-head .badge")).to_have_text("Closed")
            expect(k).to_have_url(closed_page)

        # C. The results list refreshes by itself and stops when nothing can change.
        with step("16 results refresh stops after the last submit and Close, without a reload"):
            cs = new_context(**DESKTOP).new_page()
            start(cs, base_url, live_code, "Citra Live")
            answer_first(cs)
            live_attempts = f"{base_url}/admin/exams/{live_id}/attempts"
            fetches = []
            k.on("request", lambda req: urlsplit(req.url).path == urlsplit(live_attempts).path and fetches.append(req.url))
            k.goto(live_attempts)
            expect(k.locator("#results-live")).to_have_attribute("data-live", "true")
            expect(k.locator("#results-status")).to_be_visible()
            expect(attempt_row(k, "Citra Live")).to_contain_text("In progress")
            k.evaluate("() => { window.__stay = 1; }")
            submit(cs)
            other = k.context.new_page()
            other.goto(f"{base_url}/admin/exams/{live_id}")
            state_button(other, live_id, "close").click()
            expect(other.locator(".exam-head .badge")).to_have_text("Closed")
            other.close()
            k.bring_to_front()
            expect(k.locator("#results-live")).to_have_attribute("data-live", "false", timeout=25_000)
            if k.evaluate("() => window.__stay") != 1:
                raise AssertionError("the results page was reloaded")
            expect(attempt_row(k, "Citra Live")).to_contain_text("Submitted")
            expect(k.locator("#results-status")).to_be_hidden()
            shot(k, "c-results-stopped.png")
            fetches.clear()
            k.wait_for_timeout(12_000)
            if fetches:
                raise AssertionError(f"the stopped results page fetched {len(fetches)} times: {fetches}")

        # D. A 360 × 640 phone.
        ps = new_context(**PHONE).new_page()
        with step("17 360 px /: Start above the fold; /?code= focuses the name box"):
            ps.goto(base_url + "/")
            no_page_overflow(ps)
            b = box(ps.locator("#start-form button[type=submit]"))
            if b["y"] + b["height"] > PHONE["viewport"]["height"]:
                raise AssertionError(f"Start ends at {b['y'] + b['height']:.1f} px, want at most 640")
            shot(ps, "d-landing-360.png")
            ps.goto(f"{base_url}/?code={phone_code}")
            no_page_overflow(ps)
            expect(ps.locator("input#start-code[type=text]")).to_have_count(0)
            ps.wait_for_function("() => document.activeElement && document.activeElement.id === 'name'")
            shot(ps, "d-landing-code-360.png")

        with step("18 360 px student exam: timer and save state in the window, 44 px targets; the result"):
            ps.keyboard.type("Pia Phone")
            ps.keyboard.press("Enter")
            expect(ps.locator("main#exam")).to_be_visible()
            no_page_overflow(ps)
            in_window(ps.locator("#timer"), PHONE["viewport"], "#timer")
            in_window(ps.locator("#save-state"), PHONE["viewport"], "#save-state")
            options = ps.locator("label.option")
            for i in range(options.count()):
                at_least(options.nth(i), TOUCH, f"label.option {i}")
            at_least(ps.locator("#submit-open"), TOUCH, "#submit-open")
            shot(ps, "d-student-exam-360.png")
            answer_first(ps)
            submit(ps)
            no_page_overflow(ps)
            shot(ps, "d-result-360.png")

        with step("19 360 px teacher: exams list, exam page, projector, results"):
            pt = new_context(**PHONE).new_page()
            teacher_login(pt, base_url, admin_email, "Phone Admin")
            no_page_overflow(pt)
            row_close = state_button(pt, phone_id, "close")
            at_least(row_close, TOUCH, "the row's Close button")
            ends_inside(row_close, 360, "the row's Close button")
            shot(pt, "d-exams-360.png")
            pt.goto(f"{base_url}/admin/exams/{phone_id}")
            no_page_overflow(pt)
            at_least(state_button(pt, phone_id, "close"), TOUCH, "Close exam")
            at_least(pt.locator("#projector-link"), TOUCH, "#projector-link")
            ends_inside(pt.locator("#exam-code"), 360, "#exam-code")
            shot(pt, "d-teacher-exam-360.png")
            pt.goto(f"{base_url}/admin/exams/{phone_id}/projector")
            no_page_overflow(pt)
            shot(pt, "d-projector-360.png")
            pt.goto(f"{base_url}/admin/exams/{phone_id}/attempts")
            no_page_overflow(pt)
            cells = pt.locator("#attempts tbody tr").first.locator("td")
            for i, what in [(0, "name"), (3, "status"), (4, "score")]:
                ends_inside(cells.nth(i), 360, f"the first row's {what} cell")
            shot(pt, "d-results-360.png")

        # E. Dark and light follow the device setting.
        with step("20 dark and light: no data-theme; body colours from the tokens on / and the exams list"):
            for scheme, (background, colour) in SCHEMES.items():
                page = new_context(color_scheme=scheme, **DESKTOP).new_page()
                page.goto(base_url + "/")
                for where in ("/", "/admin/exams"):
                    if where == "/admin/exams":
                        teacher_login(page, base_url, admin_email, "Scheme Admin")
                    if page.locator("html").get_attribute("data-theme") is not None:
                        raise AssertionError(f"{scheme} {where}: <html> has data-theme")
                    got = page.evaluate("() => { const s = getComputedStyle(document.body); return [s.backgroundColor, s.color]; }")
                    if got != [background, colour]:
                        raise AssertionError(f"{scheme} {where}: body is {got}, want {[background, colour]}")
                    shot(page, f"e-{scheme}-{where.strip('/').replace('/', '-') or 'root'}.png")

        with step("21 no console errors, page errors or foreign requests"):
            problems = []
            for log in logs:
                problems += [f"console error: {e}" for e in log["console_errors"]]
                problems += [f"page error: {e}" for e in log["page_errors"]]
                problems += [f"foreign request: {u}" for u in foreign_requests(log["requests"], base_url, oidc_url)]
            if problems:
                raise AssertionError("\n  ".join(["problems found:"] + problems))

        browser.close()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True)
    add_sign_in_args(parser)
    parser.add_argument("--out", type=Path, default=Path(__file__).parent / "out" / "walkthrough")
    args = parser.parse_args()
    base_url, oidc_url = args.base_url.rstrip("/"), args.oidc_url.rstrip("/")
    require_cross_site(base_url, oidc_url)
    try:
        run(base_url, args.admin_email, oidc_url, args.out)
    except StepFailed as err:
        print(err, file=sys.stderr)
        return 1
    print(f"OK: walk-through passed; screenshots in {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
