# /// script
# requires-python = ">=3.12"
# dependencies = ["playwright==1.63.0"]
# ///
"""Browser check for the exam editor in real Chromium: a new exam is written in
the browser (option buttons without scripts, Enter saves), questions are added,
edited, moved and removed, an image is added and moved into an option, settings
change, a refused save and an outdated page keep the typed text, every save is a
numbered revision with the app's summary, each revision opens as it looked, the
download imports again to an equal exam, and a student takes the edited exam.
Needs a fresh instance.

With --rollback it checks, against the previous binary (9da64ff) on a copy of
the data directory a normal run left, that the edited exam still works there.

uv run e2e/editor.py --base-url URL --admin-email EMAIL --oidc-url URL --out DIR [--rollback]
"""

import argparse
import re
import sys
import tempfile
import time
from contextlib import contextmanager
from pathlib import Path

from playwright.sync_api import Locator, Page, expect, sync_playwright

from common import (
    EXAM_CODE,
    add_sign_in_args,
    confirm,
    exam_code,
    foreign_requests,
    import_exam,
    open_exam,
    require_cross_site,
    start,
    teacher_login,
    watch,
    write_png,
)

DESKTOP = {"viewport": {"width": 1280, "height": 800}}
PHONE_VIEWPORT = {"width": 412, "height": 915}
CREATED = "Created in the editor"
IMPORTED = "Imported"
FROZEN = "This exam has attempts, so its questions are frozen."
STALE = "This exam changed since you opened this page. Nothing was saved."
TITLE = "Editor check"
FINAL_TITLE = "Editor check (final)"
STUDENTS = ["Ayu Lestari", "Budi Santoso"]
IMAGE_LINE = re.compile(r"^!\[\]\([0-9a-f]{32}\.png\)$")
# The edited exam's questions with their correct options, in page order.
KEY = [
    ("Which city is the capital of Italy?", "Rome"),
    ("What is 7 × 8?", "56"),
    ("What shape is this?", "triangle"),
]
# The edited exam's history, newest first.
HISTORY = [
    "Question 1 moved to 2",
    "Question 2 moved to 1",
    "Settings changed: title, length",
    "Question 1 removed",
    "Question 4 moved to 3",
    "Question 3 edited",
    "Question 2 edited; correct option changed",
    "Question 4 added",
    "Question 3 added",
    "Question 2 added",
    CREATED,
]


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


def history(page: Page, base_url: str, exam_id: int) -> list[list[str]]:
    """Open the exam's page, press History; returns the rows' cells (number, saved, teacher, summary)."""
    page.goto(f"{base_url}/admin/exams/{exam_id}")
    page.locator("#history-link").click()
    expect(page).to_have_url(re.compile(rf"/admin/exams/{exam_id}/revisions$"))
    expect(page.locator("#revisions")).to_be_visible()
    rows = page.locator("#revisions tbody tr[data-revision]")
    return [[c.strip() for c in rows.nth(i).locator("td").all_inner_texts()[:4]] for i in range(rows.count())]


def latest(page: Page, base_url: str, exam_id: int, summary: str, count: int) -> None:
    """The exam's history has count rows and the newest one is summary."""
    rows = history(page, base_url, exam_id)
    if len(rows) != count or rows[0][3] != summary:
        raise AssertionError(f"history {[(r[0], r[3]) for r in rows]}, want {count} rows, newest {summary!r}")


def preview(page: Page) -> list[tuple[str, str]]:
    """(question text, correct option text) of every question on the page, in order."""
    questions = page.locator("section.preview-question")
    result = []
    for i in range(questions.count()):
        q = questions.nth(i)
        expect(q.locator(".correct-mark")).to_have_count(1)
        result.append(
            (q.locator(".question-body").inner_text().strip(), q.locator("li[data-correct=true] .option-body").inner_text().strip())
        )
    return result


def loaded_width(image: Locator) -> int:
    """naturalWidth of image, after it has loaded."""
    image.evaluate(
        "img => img.complete ? null : new Promise((ok, fail) => { img.onload = ok; img.onerror = fail; })"
    )
    return image.evaluate("img => img.naturalWidth")


def image_width(scope: Locator) -> int:
    """naturalWidth of the one image inside scope."""
    images = scope.locator("img")
    expect(images).to_have_count(1)
    return loaded_width(images.first)


def want_width(scope: Locator, width: int) -> None:
    if (got := image_width(scope)) != width:
        raise AssertionError(f"image is {got} px wide, want {width}")


def option_values(form: Locator) -> list[str]:
    boxes = form.locator("textarea[name=option]")
    return [boxes.nth(i).input_value() for i in range(boxes.count())]


def option_button(form: Locator, index: int, name: str) -> Locator:
    return form.locator(".option-row").nth(index).get_by_role("button", name=name, exact=True)


def fill_question(form: Locator, text: str, options: list[str]) -> None:
    """Type the question text and the first len(options) options."""
    form.locator("#question-text").fill(text)
    boxes = form.locator("textarea[name=option]")
    for i, option in enumerate(options):
        boxes.nth(i).fill(option)


def choose_correct(form: Locator, index: int) -> None:
    form.locator(".option-row").nth(index).locator("input[type=radio][name=correct]").check()


def timed_save(page: Page, exam_id: int, n: int) -> float:
    """Press Save question; seconds from the click until the URL is the editor page at question n."""
    began = time.perf_counter()
    page.locator("#save").click()
    page.wait_for_url(re.compile(rf"/admin/exams/{exam_id}/edit#q{n}$"), wait_until="commit")
    took = time.perf_counter() - began
    expect(page.locator(".editor-page")).to_be_visible()
    return took


def save(page: Page, exam_id: int, n: int) -> None:
    page.locator("#save").click()
    expect(page).to_have_url(re.compile(rf"/admin/exams/{exam_id}/edit#q{n}$"))


def editor(page: Page, base_url: str, exam_id: int) -> None:
    page.goto(f"{base_url}/admin/exams/{exam_id}/edit")
    expect(page.locator(".editor-page")).to_be_visible()


def press_in_editor(page: Page, exam_id: int, selector: str) -> None:
    """Press a button on the editor page; lands on the editor page again."""
    page.locator(selector).click()
    expect(page).to_have_url(re.compile(rf"/admin/exams/{exam_id}/edit$"))


def add_question(page: Page, exam_id: int, n: int) -> Locator:
    page.locator("#add-question").click()
    expect(page).to_have_url(re.compile(rf"/admin/exams/{exam_id}/questions/new$"))
    expect(page.locator("h1")).to_have_text(f"New question {n}")
    return page.locator("#question-form")


def open_question(page: Page, exam_id: int, n: int) -> Locator:
    page.locator(f"#q{n} a.edit-question").click()
    expect(page).to_have_url(re.compile(rf"/admin/exams/{exam_id}/questions/{n}$"))
    return page.locator("#question-form")


def download(page: Page, base_url: str, exam_id: int, dir: Path) -> Path:
    """Press Download on the exam's page; returns the saved file, named as the server suggested."""
    page.goto(f"{base_url}/admin/exams/{exam_id}")
    with page.expect_download() as info:
        page.locator("#download-link").click()
    path = dir / info.value.suggested_filename
    info.value.save_as(path)
    return path


def take_exam(page: Page, base_url: str, code: str, name: str) -> None:
    """A student starts with code, picks every correct option of KEY, submits and sees 3 / 3."""
    start(page, base_url, code, name)
    key = dict(KEY)
    sections = page.locator("section.question")
    expect(sections).to_have_count(len(KEY))
    for n in range(len(KEY)):
        section = sections.nth(n)
        body = section.locator(".question-body").inner_text()
        text = next(q for q in key if q in body)
        labels = section.locator("label.option")
        for i in range(labels.count()):
            if labels.nth(i).locator(".option-body").inner_text().strip() == key[text]:
                labels.nth(i).click()
                break
        else:
            raise AssertionError(f"question {text!r} has no option {key[text]!r}")
        expect(section.locator(".q-state")).to_have_attribute("data-state", "saved")
    expect(page.locator("#save-state")).to_have_attribute("data-state", "saved")
    page.locator("#submit-open").click()
    expect(page.locator("#submit-dialog")).to_be_visible()
    page.locator("#submit-confirm").click()
    expect(page.locator("main#result")).to_be_visible()
    expect(page.locator("#score")).to_have_text(f"{len(KEY)} / {len(KEY)}")


def attempt_row(page: Page, base_url: str, exam_id: int, name: str) -> Locator:
    page.goto(f"{base_url}/admin/exams/{exam_id}/attempts")
    return page.locator("#attempts tbody tr[data-attempt-id]").filter(has_text=name)


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

        with step("2 New exam: option buttons keep the typed text, Enter in the title creates the exam"):
            a.locator("#new-exam").click()
            expect(a).to_have_url(re.compile(r"/admin/exams/new$"))
            form = a.locator("#new-exam-form")
            form.locator("#exam-title").fill(TITLE)
            form.locator("#exam-shuffle").select_option("none")
            form.locator("#exam-show-result").select_option("review")
            question = "Which number is even?"
            fill_question(form, question, ["5", "3", "4"])

            def kept(options: list[str]) -> None:
                expect(a).to_have_url(re.compile(r"/admin/exams/new$"))
                expect(form.locator("#exam-title")).to_have_value(TITLE)
                expect(form.locator("#exam-shuffle")).to_have_value("none")
                expect(form.locator("#exam-show-result")).to_have_value("review")
                expect(form.locator("#question-text")).to_have_value(question)
                expect(form.locator("textarea[name=option]")).to_have_count(len(options))
                if (got := option_values(form)) != options:
                    raise AssertionError(f"options {got}, want {options}")

            kept(["5", "3", "4", ""])
            option_button(form, 3, "Remove").click()
            kept(["5", "3", "4"])
            option_button(form, 0, "Down").click()
            kept(["3", "5", "4"])
            option_button(form, 1, "Down").click()
            kept(["3", "4", "5"])
            form.get_by_role("button", name="Add option", exact=True).click()
            kept(["3", "4", "5", ""])
            option_button(form, 3, "Remove").click()
            kept(["3", "4", "5"])
            shot(a, "desktop-new-exam.png")
            choose_correct(form, 1)
            form.locator("#exam-title").press("Enter")
            expect(a).to_have_url(re.compile(r"/admin/exams/\d+/edit$"))
            exam_id = int(re.search(r"/admin/exams/(\d+)/edit$", a.url).group(1))
            expect(a.locator("#q1 li[data-correct=true] .option-body")).to_have_text("4")
            rows = history(a, base_url, exam_id)
            if [(r[0], r[2], r[3]) for r in rows] != [("1", admin_email, CREATED)]:
                raise AssertionError(f"history {rows}, want one row 1 {admin_email} {CREATED!r}")

        with step("3 add question 2"):
            editor(a, base_url, exam_id)
            form = add_question(a, exam_id, 2)
            fill_question(form, "Which city is the capital of France?", ["Paris", "Lyon", "Nice", "Lille"])
            choose_correct(form, 0)
            save(a, exam_id, 2)
            latest(a, base_url, exam_id, "Question 2 added", 2)

        with step("4 add question 3 with an image"):
            editor(a, base_url, exam_id)
            form = add_question(a, exam_id, 3)
            fill_question(form, "What shape is this?", ["square", "triangle"])
            option_button(form, 3, "Remove").click()
            option_button(form, 2, "Remove").click()
            expect(form.locator("textarea[name=option]")).to_have_count(2)
            if (got := option_values(form)) != ["square", "triangle"]:
                raise AssertionError(f"options {got} after removing two")
            choose_correct(form, 1)
            form.locator("#question-image").set_input_files(str(write_png(tmp / "shape.png", 160, 120)))
            print(f"save time, step 4: {timed_save(a, exam_id, 3):.3f} s", flush=True)
            want_width(a.locator("#q3"), 160)
            shot(a, "desktop-editor.png")
            form = open_question(a, exam_id, 3)
            text = form.locator("#question-text").input_value()
            if not IMAGE_LINE.match(text.split("\n")[-1]):
                raise AssertionError(f"question 3 text {text!r} does not end with a line ![](<32 hex>.png)")
            image_line = text.split("\n")[-1]
            shot(a, "desktop-question.png")
            latest(a, base_url, exam_id, "Question 3 added", 3)

        with step("5 add question 4"):
            editor(a, base_url, exam_id)
            form = add_question(a, exam_id, 4)
            fill_question(form, "What is 7 × 8?", ["54", "56", "64", "48"])
            choose_correct(form, 1)
            save(a, exam_id, 4)
            latest(a, base_url, exam_id, "Question 4 added", 4)

        with step("6 edit question 2: another question, its correct option rewritten"):
            editor(a, base_url, exam_id)
            form = open_question(a, exam_id, 2)
            form.locator("#question-text").fill("Which city is the capital of Italy?")
            form.locator("textarea[name=option]").nth(0).fill("Rome")
            save(a, exam_id, 2)
            latest(a, base_url, exam_id, "Question 2 edited; correct option changed", 5)

        with step("7 edit question 3: the image line moves into option 1"):
            editor(a, base_url, exam_id)
            form = open_question(a, exam_id, 3)
            text = form.locator("#question-text").input_value()
            lines = text.split("\n")
            if lines[-1] != image_line:
                raise AssertionError(f"question 3 text {text!r} no longer ends with {image_line!r}")
            form.locator("#question-text").fill("\n".join(lines[:-1]).rstrip("\n"))
            first = form.locator("textarea[name=option]").nth(0)
            first.fill(first.input_value() + "\n" + image_line)
            print(f"save time, step 7: {timed_save(a, exam_id, 3):.3f} s", flush=True)
            expect(a.locator("#q3 .question-body img")).to_have_count(0)
            want_width(a.locator("#q3 .preview-options li").nth(0), 160)
            latest(a, base_url, exam_id, "Question 3 edited", 6)

        with step("8 move question 4 up, remove question 1, change title and length"):
            editor(a, base_url, exam_id)
            press_in_editor(a, exam_id, "#q4 form.move-up button")
            latest(a, base_url, exam_id, "Question 4 moved to 3", 7)
            editor(a, base_url, exam_id)
            a.locator("#q1 a.remove-question").click()
            expect(a).to_have_url(re.compile(rf"/admin/exams/{exam_id}/questions/1/remove$"))
            confirm(a, "remove-question-form")
            expect(a).to_have_url(re.compile(rf"/admin/exams/{exam_id}/edit$"))
            latest(a, base_url, exam_id, "Question 1 removed", 8)
            editor(a, base_url, exam_id)
            a.locator("#settings-link").click()
            expect(a).to_have_url(re.compile(rf"/admin/exams/{exam_id}/settings$"))
            form = a.locator("#settings-form")
            form.locator("#exam-title").fill(FINAL_TITLE)
            form.locator("#exam-duration").fill("45")
            shot(a, "desktop-settings.png")
            press_in_editor(a, exam_id, "#settings-form button[type=submit]")
            if (got := preview(a)) != KEY:
                raise AssertionError(f"editor shows {got}, want {KEY}")
            latest(a, base_url, exam_id, "Settings changed: title, length", 9)

        with step("9 a refused save answers the form with the typed text"):
            editor(a, base_url, exam_id)
            form = open_question(a, exam_id, 1)
            form.locator("#question-text").fill("")
            form.locator("textarea[name=option]").nth(1).fill("Milan")
            a.locator("#save").click()
            expect(a.locator("[role=alert]")).to_have_text("Not saved: question 1 has no text")
            expect(a).to_have_url(re.compile(rf"/admin/exams/{exam_id}/questions/1$"))
            expect(form.locator("textarea[name=option]").nth(1)).to_have_value("Milan")
            expect(form.locator("#question-text")).to_have_value("")
            shot(a, "desktop-refused.png")
            if len(history(a, base_url, exam_id)) != 9:
                raise AssertionError("the refused save changed the history")

        with step("10 a save from an outdated page is refused and keeps the typed text"):
            editor(a, base_url, exam_id)
            form = open_question(a, exam_id, 1)
            b = a.context.new_page()
            editor(b, base_url, exam_id)
            press_in_editor(b, exam_id, "#q2 form.move-up button")
            b.close()
            typed = "Which city is the capital of Italy, the country?"
            form.locator("#question-text").fill(typed)
            a.locator("#save").click()
            expect(a.locator("[role=alert]")).to_have_text(STALE)
            expect(a).to_have_url(re.compile(rf"/admin/exams/{exam_id}/questions/1$"))
            expect(form.locator("#question-text")).to_have_value(typed)
            shot(a, "desktop-stale.png")
            latest(a, base_url, exam_id, "Question 2 moved to 1", 10)
            editor(a, base_url, exam_id)
            press_in_editor(a, exam_id, "#q1 form.move-down button")
            latest(a, base_url, exam_id, "Question 1 moved to 2", 11)

        with step("11 History lists the eleven saves newest first, each by the admin"):
            rows = history(a, base_url, exam_id)
            got = [(r[0], r[2], r[3]) for r in rows]
            want = [(str(len(HISTORY) - i), admin_email, s) for i, s in enumerate(HISTORY)]
            if got != want:
                raise AssertionError(f"history rows {got}, want {want}")
            shot(a, "desktop-history.png")

        with step("12 revisions open as they looked"):
            for n, title, count in [(1, TITLE, 1), (3, TITLE, 3), (6, TITLE, 4), (8, TITLE, 3), (9, FINAL_TITLE, 3)]:
                history(a, base_url, exam_id)
                a.locator(f"#revisions tr[data-revision='{n}'] a").click()
                expect(a).to_have_url(re.compile(rf"/admin/exams/{exam_id}/revisions/{n}$"))
                expect(a.locator("h1")).to_have_text(f"Revision {n}")
                expect(a.locator("dl.settings dd").first).to_have_text(title)
                expect(a.locator("section.preview-question")).to_have_count(count)
                expect(a.locator("main form")).to_have_count(0)
                if n == 3:
                    want_width(a.locator("#q3 .question-body"), 160)
                    expect(a.locator("#q3 .preview-options img")).to_have_count(0)
                if n == 6:
                    expect(a.locator("#q3 .question-body img")).to_have_count(0)
                    want_width(a.locator("#q3 .preview-options"), 160)
                if n == 9:
                    expect(a.locator("dl.settings dd").nth(1)).to_have_text("45 minutes")
                    shot(a, "desktop-revision-9.png")

        with step("13 the download imports again as an equal exam"):
            a.goto(f"{base_url}/admin/exams/{exam_id}")
            code = a.locator("#exam-code").inner_text().strip()
            if not EXAM_CODE.match(code):
                raise AssertionError(f"exam has code {code!r}")
            edited = preview(a)
            if edited != KEY:
                raise AssertionError(f"exam page shows {edited}, want {KEY}")
            want_width(a.locator("section.preview-question"), 160)
            (tmp / "downloads").mkdir()
            zip_path = download(a, base_url, exam_id, tmp / "downloads")
            if zip_path.name != f"exam-{code}.zip":
                raise AssertionError(f"download named {zip_path.name!r}, want exam-{code}.zip")
            a.goto(f"{base_url}/admin/exams")
            again_id = import_exam(a, zip_path)
            expect(a.locator(".exam-head h1")).to_have_text(FINAL_TITLE)
            if (got := preview(a)) != edited:
                raise AssertionError(f"re-imported preview {got}, want {edited}")
            want_width(a.locator("section.preview-question"), 160)
            rows = history(a, base_url, again_id)
            if [r[3] for r in rows] != [IMPORTED]:
                raise AssertionError(f"re-imported history {rows}, want one row {IMPORTED}")

        with step("14 a student takes the edited exam; it is frozen afterwards"):
            a.goto(f"{base_url}/admin/exams/{exam_id}")
            open_exam(a, exam_id)
            s = new_context(**phone).new_page()
            take_exam(s, base_url, code, STUDENTS[0])
            expect(attempt_row(a, base_url, exam_id, STUDENTS[0])).to_contain_text("3 / 3")
            a.goto(f"{base_url}/admin/exams/{exam_id}")
            expect(a.locator("#edit-link")).to_have_count(0)
            response = a.goto(f"{base_url}/admin/exams/{exam_id}/edit")
            if response.status != 409:
                raise AssertionError(f"editor of a frozen exam answers {response.status}, want 409")
            expect(a.locator(".exam-head h1")).to_have_text(FINAL_TITLE)
            expect(a.locator("[role=alert]")).to_have_text(FROZEN)
            shot(a, "desktop-frozen.png")

        with step("15 editor page, question form and new-exam form fit a phone"):
            pages = [
                ("editor", f"/admin/exams/{again_id}/edit"),
                ("question", f"/admin/exams/{again_id}/questions/1"),
                ("new-exam", "/admin/exams/new"),
            ]
            for size, viewport in [("desktop", DESKTOP["viewport"]), ("phone", PHONE_VIEWPORT)]:
                a.set_viewport_size(viewport)
                for name, path in pages:
                    a.goto(base_url + path)
                    expect(a.locator("main")).to_be_visible()
                    if size == "phone":
                        no_page_overflow(a)
                    shot(a, f"{size}-{name}-fit.png")
            a.set_viewport_size(DESKTOP["viewport"])

        with step("16 no page errors or foreign requests; console errors are the three expected page loads"):
            check_logs(
                logs,
                base_url,
                oidc_url,
                [
                    console_error(f"{base_url}/admin/exams/{exam_id}/questions/1", 422, "Unprocessable Entity"),
                    console_error(f"{base_url}/admin/exams/{exam_id}/questions/1", 409, "Conflict"),
                    console_error(f"{base_url}/admin/exams/{exam_id}/edit", 409, "Conflict"),
                ],
            )

        browser.close()


def run_rollback(base_url: str, admin_email: str, oidc_url: str, out: Path) -> None:
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

        a = new_context(**DESKTOP).new_page()
        with step("R1 admin signs in"):
            teacher_login(a, base_url, admin_email, "Ada Admin")

        with step("R2 two exams are titled Editor check (final); the open one with one attempt is the edited exam"):
            rows = a.locator("#exams tbody tr[data-exam-id]").filter(has=a.get_by_role("cell", name=FINAL_TITLE, exact=True))
            expect(rows).to_have_count(2)
            edited = rows.filter(has=a.locator(".badge", has_text="Open"))
            expect(edited).to_have_count(1)
            expect(edited.locator("td.numeric").nth(1)).to_have_text("1")
            exam_id = int(edited.get_attribute("data-exam-id"))
            code = exam_code(a, exam_id)
            a.screenshot(path=out / "desktop-exams.png", full_page=True, animations="disabled")

        with step("R3 the exam page previews the three edited questions and the image"):
            a.goto(f"{base_url}/admin/exams/{exam_id}")
            expect(a.locator(".exam-head h1")).to_have_text(FINAL_TITLE)
            if (got := preview(a)) != KEY:
                raise AssertionError(f"exam page shows {got}, want {KEY}")
            want_width(a.locator("#q3 .preview-options"), 160)
            a.screenshot(path=out / "desktop-exam.png", full_page=True, animations="disabled")

        with step("R4 History has the eleven rows"):
            if (got := [r[3] for r in history(a, base_url, exam_id)]) != HISTORY:
                raise AssertionError(f"history {got}, want {HISTORY}")

        with step("R5 Download gives exam-<CODE>.zip"):
            path = download(a, base_url, exam_id, tmp)
            if path.name != f"exam-{code}.zip":
                raise AssertionError(f"download named {path.name!r}, want exam-{code}.zip")

        with step("R6 a second student takes the exam and scores 3 / 3; the results list has two attempts"):
            s = new_context(**phone).new_page()
            take_exam(s, base_url, code, STUDENTS[1])
            expect(attempt_row(a, base_url, exam_id, STUDENTS[1])).to_contain_text("3 / 3")
            expect(a.locator("#attempts tbody tr[data-attempt-id]")).to_have_count(2)

        with step("R7 the previous version has no editor: its page is 404"):
            response = a.goto(f"{base_url}/admin/exams/{exam_id}/edit")
            if response.status != 404:
                raise AssertionError(f"/admin/exams/{exam_id}/edit answers {response.status}, want 404")

        with step("R8 no page errors or foreign requests; the one console error is the 404"):
            check_logs(logs, base_url, oidc_url, [console_error(f"{base_url}/admin/exams/{exam_id}/edit", 404, "Not Found")])

        browser.close()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", required=True)
    add_sign_in_args(parser)
    parser.add_argument("--out", type=Path, default=Path(__file__).parent / "out" / "editor")
    parser.add_argument("--rollback", action="store_true", help="check the previous binary on the data a normal run left")
    args = parser.parse_args()
    base_url, oidc_url = args.base_url.rstrip("/"), args.oidc_url.rstrip("/")
    require_cross_site(base_url, oidc_url)
    try:
        (run_rollback if args.rollback else run)(base_url, args.admin_email, oidc_url, args.out)
    except StepFailed as err:
        print(err, file=sys.stderr)
        return 1
    mode = "rollback check" if args.rollback else "editor check"
    print(f"OK: {mode} passed; screenshots in {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
