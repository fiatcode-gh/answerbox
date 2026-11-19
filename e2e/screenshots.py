# /// script
# requires-python = ">=3.12"
# dependencies = ["playwright==1.63.0"]
# ///
"""Screenshot set of Answerbox in real Chromium: every page in the light and
dark colour schemes at 412 and 1280 px wide, and the classroom pages at 360 px,
each checked for sideways page scroll. With --readme, also the ten viewport-sized
pictures the README shows. Needs a fresh instance.

uv run e2e/screenshots.py --base-url URL --admin-email EMAIL --oidc-url URL --out DIR [--readme DIR]
"""

import argparse
import re
import sys
import tempfile
import zipfile
from contextlib import contextmanager
from datetime import datetime, timedelta
from pathlib import Path
from zoneinfo import ZoneInfo

from playwright.sync_api import Page, TimeoutError as PlaywrightTimeout, expect, sync_playwright

from common import (
    add_sign_in_args,
    confirm,
    foreign_requests,
    import_exam,
    open_exam,
    require_cross_site,
    start,
    teacher_login,
    watch,
    write_png,
)

THEMES = ("light", "dark")
VIEWPORTS = {
    360: {"width": 360, "height": 640},
    412: {"width": 412, "height": 860},
    1280: {"width": 1280, "height": 800},
}
BOTH = (412, 1280)
CLASSROOM = (360, 412, 1280)
INPUT_LAYOUT = "%Y-%m-%dT%H:%M"
MISSING = "/no-such-page"
SECOND_TEACHER = "teacher@example.test"

TITLE_A = "Mathematics, chapter 3"
TITLE_B = "Geography check"
TITLE_C = "Science quiz"
TITLE_D = "History, term 1"

EXAM_A = """# Mathematics, chapter 3
shuffle: none
show_result: review

## Part A: Shapes and numbers
Choose **one** answer for each question.

1. What is the area of this right triangle with legs 3 and 4?
![A right triangle with legs 3 and 4](triangle.png)
- 6 *
- 7
- 12
- 14

2. How many sides does a hexagon have?
- 6 *
- 5
- 7
- 8

3. What is 7 × 8?
- 56 *
- 54
- 48
- 64

## Part B: Short answers
Type the number only.

4. What is 15 + 27?
= 42
"""

EXAM_C = """# Science quiz
show_result: score

1. Which planet is closest to the Sun?
- Mercury *
- Venus
- Earth
- Mars

2. What does a plant take in from the air to make its food?
- Carbon dioxide *
- Oxygen
- Nitrogen
"""

EXAM_D = """# History, term 1

1. In which year did Indonesia proclaim its independence?
- 1945 *
- 1949
- 1965

2. Which city was the capital of Indonesia from 1946 to 1949?
- Yogyakarta *
- Bandung
- Surabaya
"""

# README pictures: (page name, theme, width) → file name.
README = {
    ("landing", "light", 412): "landing-phone-light.png",
    ("student-exam", "light", 412): "student-exam-phone-light.png",
    ("student-exam", "dark", 412): "student-exam-phone-dark.png",
    ("student-result", "light", 412): "student-result-phone-light.png",
    ("exams", "light", 1280): "exams-desktop-light.png",
    ("exam-open", "light", 1280): "exam-desktop-light.png",
    ("exam-open", "dark", 1280): "exam-desktop-dark.png",
    ("results", "light", 1280): "results-desktop-light.png",
    ("editor", "light", 1280): "editor-desktop-light.png",
    ("projector", "dark", 1280): "projector-desktop-dark.png",
}


class StepFailed(Exception):
    pass


@contextmanager
def step(name: str):
    print(f"step: {name}", flush=True)
    try:
        yield
    except Exception as err:  # every failure names its step
        raise StepFailed(f"FAIL [{name}]: {err}") from err


def overflow(page: Page) -> str | None:
    """None when the document has no sideways scroll, else the two widths."""
    scroll, client = page.evaluate(
        "() => [document.documentElement.scrollWidth, document.documentElement.clientWidth]"
    )
    return None if scroll <= client else f"scrollWidth {scroll} > clientWidth {client}"


def pick(page: Page, question: int, option: str) -> None:
    """Pick the option with this text in question N and wait until it is saved."""
    q = page.locator(f"section.question#q{question}")
    q.locator("label.option").filter(has=page.locator(".option-body", has_text=re.compile(rf"^\s*{re.escape(option)}\s*$"))).click()
    expect(q.locator(".q-state")).to_have_attribute("data-state", "saved")


# Runs after the page's own handlers (window is the last bubbling target) and
# records whether the page blocked the paste.
PROBE_JS = """() => {
  window.__blocked = {};
  window.addEventListener("paste", e => { window.__blocked.paste = e.defaultPrevented; });
}"""


def blocked_paste(page: Page) -> None:
    """Press Ctrl+V on question 3's text; the monitor must block it."""
    page.evaluate(PROBE_JS)
    body = page.locator("section.question#q3 .question-body")
    body.click()
    page.keyboard.press("Control+V")
    try:
        page.wait_for_function("'paste' in window.__blocked", timeout=2000)
    except PlaywrightTimeout:
        # Some Chromium builds fire no paste event without clipboard content;
        # deliver the event the browser would.
        print("  note: Ctrl+V fired no paste event; dispatching one", flush=True)
        body.evaluate("el => el.dispatchEvent(new ClipboardEvent('paste', {bubbles: true, cancelable: true}))")
    if not page.evaluate("window.__blocked.paste"):
        raise AssertionError("the paste was not blocked")


def build_exam_a(dir: Path) -> Path:
    path = dir / "mathematics.zip"
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as z:
        z.writestr("exam.md", EXAM_A)
        z.write(write_png(dir / "triangle.png", 240, 180), "triangle.png")
    return path


def write_text(dir: Path, name: str, text: str) -> Path:
    path = dir / name
    path.write_text(text, encoding="utf-8")
    return path


def fill_options(form, texts: list[str]) -> None:
    """Fill the four option rows a new question form shows."""
    rows = form.locator("textarea[name=option]")
    expect(rows).to_have_count(len(texts))
    for i, text in enumerate(texts):
        rows.nth(i).fill(text)


def save_question(page: Page, exam_id: int) -> None:
    page.locator("#save").click()
    expect(page).to_have_url(re.compile(rf"/admin/exams/{exam_id}/edit(#.*)?$"))


def choice_question(page: Page, exam_id: int, text: str, options: list[str]) -> None:
    """Add Question → a multiple-choice question whose first option is correct."""
    page.locator("#add-question").click()
    form = page.locator("#question-form")
    form.locator("#question-text").fill(text)
    fill_options(form, options)
    form.locator(".option-row").nth(0).locator("input[name=correct]").check()
    save_question(page, exam_id)


def start_section(page: Page, exam_id: int, question: int, title: str, text: str) -> None:
    page.locator(f"#q{question} a.start-section").click()
    page.locator("#section-title").fill(title)
    page.locator("#section-text").fill(text)
    page.locator("#section-form #save").click()
    expect(page).to_have_url(re.compile(rf"/admin/exams/{exam_id}/edit(#.*)?$"))


def run(base_url: str, admin_email: str, oidc_url: str, out: Path, readme: Path | None) -> list[str]:
    """Builds the state, takes the pictures; returns the pages that overflow."""
    out.mkdir(parents=True, exist_ok=True)
    if readme is not None:
        readme.mkdir(parents=True, exist_ok=True)
    logs, overflows = [], []
    missing_loads = 0
    with sync_playwright() as p, tempfile.TemporaryDirectory() as tmp:
        browser = p.chromium.launch(headless=True)
        tmp = Path(tmp)

        def new_context(**kwargs):
            context = browser.new_context(**kwargs)
            logs.append(watch(context, base_url))
            return context

        def pictures(name: str, path: str, state=None, widths=BOTH, before=None, full_page=True) -> None:
            """Picture path in both schemes at each width (state: a storage
            state, or None for a signed-out visitor). The scheme is set before
            the page loads: Chromium keeps the old colours of a file input's
            button when the scheme changes on a loaded page."""
            nonlocal missing_loads
            for width in widths:
                for theme in THEMES:
                    context = new_context(viewport=VIEWPORTS[width], storage_state=state)
                    page = context.new_page()
                    page.emulate_media(color_scheme=theme)
                    page.goto(base_url + path)
                    if path == MISSING:
                        missing_loads += 1
                    if before is not None:
                        before(page)
                    page.screenshot(path=out / f"{theme}-{width}-{name}.png", full_page=full_page, animations="disabled")
                    if (problem := overflow(page)) is not None:
                        overflows.append(f"{theme}-{width}-{name} ({path}): {problem}")
                    if readme is not None and (file := README.get((name, theme, width))):
                        page.screenshot(path=readme / file, animations="disabled")
                    context.close()

        t = new_context(viewport=VIEWPORTS[1280]).new_page()
        with step("1 the admin signs in; the empty exams list"):
            teacher_login(t, base_url, admin_email, "Ada Admin")
            if t.get_by_text("No exams yet.").count() != 1:
                raise AssertionError("not a fresh instance: the exams page does not say 'No exams yet.'")
            pictures("exams-empty", "/admin/exams", t.context.storage_state())

        with step("2 exam A imported from a zip and opened"):
            a_id = import_exam(t, build_exam_a(tmp))
            expect(t.locator(".exam-head h1")).to_have_text(TITLE_A)
            code_a = t.locator("#exam-code").inner_text().strip()
            open_exam(t, a_id)

        with step("3 Ayu answers on a phone (right, wrong, unanswered, typed) and submits"):
            ayu = new_context(viewport=VIEWPORTS[412]).new_page()
            start(ayu, base_url, code_a, "Ayu Lestari")
            pick(ayu, 1, "6")
            pick(ayu, 2, "5")
            typed = ayu.locator("section.question#q4 input.answer-text")
            typed.fill("42")
            typed.blur()
            expect(ayu.locator("section.question#q4 .q-state")).to_have_attribute("data-state", "saved")
            ayu.locator("#submit-open").click()
            ayu.locator("#submit-confirm").click()
            expect(ayu.locator("main#result")).to_be_visible()
            ayu_state = ayu.context.storage_state()
            ayu.context.close()

        with step("4 Budi on a phone: two answers and a blocked paste, still in progress"):
            budi = new_context(viewport=VIEWPORTS[412]).new_page()
            start(budi, base_url, code_a, "Budi Santoso")
            pick(budi, 1, "6")
            pick(budi, 2, "6")
            blocked_paste(budi)
            row = t.locator("#attempts tbody tr").filter(has_text="Budi Santoso")
            t.goto(f"{base_url}/admin/exams/{a_id}/attempts")
            for _ in range(20):
                if row.locator("td.flags").get_attribute("data-flags") == "1":
                    break
                t.wait_for_timeout(500)
                t.reload()
            expect(row.locator("td.flags")).to_have_attribute("data-flags", "1")
            budi_state = budi.context.storage_state()
            budi.context.close()

        with step("5 exam B written in the editor: two sections, a fill-in question; closed"):
            t.goto(f"{base_url}/admin/exams")
            t.locator("#new-exam").click()
            form = t.locator("#new-exam-form")
            form.locator("#exam-title").fill(TITLE_B)
            form.locator("#exam-show-result").select_option("review")
            form.locator("#question-text").fill("Which is the largest ocean on Earth?")
            fill_options(form, ["The Pacific Ocean", "The Atlantic Ocean", "The Indian Ocean", "The Arctic Ocean"])
            form.locator(".option-row").nth(0).locator("input[name=correct]").check()
            form.locator("#save").click()
            expect(t).to_have_url(re.compile(r"/admin/exams/\d+/edit$"))
            b_id = int(re.search(r"/admin/exams/(\d+)/edit$", t.url).group(1))
            t.locator("#add-question").click()
            t.get_by_role("button", name="Switch to fill-in", exact=True).click()
            expect(t.locator(".question-kind")).to_contain_text("Fill-in")
            t.locator("#question-text").fill("What is the capital of Indonesia?")
            t.locator(".answer-list input[name=answer]").first.fill("Jakarta")
            save_question(t, b_id)
            choice_question(t, b_id, "On which continent is Egypt?", ["Africa", "Asia", "Europe", "South America"])
            choice_question(t, b_id, "Which country has the most people?", ["India", "Brazil", "Japan", "Nigeria"])
            start_section(t, b_id, 1, "Part 1: Oceans and cities", "Read each question carefully.")
            start_section(t, b_id, 3, "Part 2: Countries", "Pick the best answer.")
            expect(t.locator(".editor-section")).to_have_count(2)

        with step("6 exam C imported with a schedule one day ahead; exam D imported and archived"):
            t.goto(f"{base_url}/admin/exams")
            c_id = import_exam(t, write_text(tmp, "science.md", EXAM_C))
            zone = ZoneInfo(re.search(r"Times are shown in (\S+?)\.\s", t.locator(".zone-note").inner_text()).group(1))
            opens = (datetime.now(zone) + timedelta(days=1)).replace(second=0, microsecond=0)
            t.locator("#schedule-details > summary").click()
            t.locator("#schedule-opens").fill(opens.strftime(INPUT_LAYOUT))
            t.locator("#schedule-form button[type=submit]").click()
            expect(t.locator("#schedule-times")).to_contain_text("Opens")
            t.goto(f"{base_url}/admin/exams")
            d_id = import_exam(t, write_text(tmp, "history.md", EXAM_D))
            t.goto(f"{base_url}/admin/exams/{d_id}/archive")
            confirm(t, "archive-form")
            expect(t.locator(".exam-head .badge")).to_have_text("Archived")

        with step("7 a second teacher on the Teachers page"):
            t.goto(f"{base_url}/admin/teachers")
            t.locator("#add-teacher-form input[name=email]").fill(SECOND_TEACHER)
            t.locator("#add-teacher-form button[type=submit]").click()
            teacher_row = t.locator("#teachers tbody tr").filter(has_text=SECOND_TEACHER)
            expect(teacher_row).to_have_count(1)
            teacher_id = teacher_row.get_attribute("data-teacher-id")

        with step("8 pictures of the student pages"):
            pictures("landing", "/", widths=CLASSROOM)
            pictures("landing-code", f"/?code={code_a}", widths=CLASSROOM)
            pictures("landing-refused", "/?code=ZZZZZZ")
            pictures("not-found", MISSING)
            pictures("sign-in", "/admin/login")
            pictures("student-exam", "/exam", budi_state, CLASSROOM)

            def open_navigator(page: Page) -> None:
                page.locator("#nav-toggle").click()
                expect(page.locator("#question-nav")).to_have_attribute("data-open", "true")

            def open_submit(page: Page) -> None:
                page.locator("#submit-open").click()
                expect(page.locator("#submit-dialog")).to_be_visible()

            pictures("student-navigator", "/exam", budi_state, (412,), open_navigator)
            # A full-page capture moves the modal dialog and its backdrop away
            # from the window the student sees; the dialog is pictured as shown.
            pictures("student-submit", "/exam", budi_state, before=open_submit, full_page=False)
            pictures("student-result", "/exam", ayu_state, CLASSROOM)

        teacher = t.context.storage_state()
        with step("9 pictures of the exams list and the exam pages"):
            pictures("exams", "/admin/exams", teacher, CLASSROOM)
            pictures("exams-archived", "/admin/exams?archived=1", teacher)
            pictures("exam-open", f"/admin/exams/{a_id}", teacher, CLASSROOM)
            pictures("exam-scheduled", f"/admin/exams/{c_id}", teacher)
            pictures("exam-archived", f"/admin/exams/{d_id}", teacher)
            pictures("projector", f"/admin/exams/{a_id}/projector", teacher, CLASSROOM)

        with step("10 pictures of the results, the attempt and Delete; Budi still has one flag"):
            t.goto(f"{base_url}/admin/exams/{a_id}/attempts")
            budi_row = t.locator("#attempts tbody tr").filter(has_text="Budi Santoso")
            expect(budi_row.locator("td.flags")).to_have_attribute("data-flags", "1")
            ayu_id = t.locator("#attempts tbody tr").filter(has_text="Ayu Lestari").get_attribute("data-attempt-id")
            pictures("results", f"/admin/exams/{a_id}/attempts", teacher, CLASSROOM)
            pictures("attempt", f"/admin/exams/{a_id}/attempts/{ayu_id}", teacher)
            pictures("confirm-delete-attempt", f"/admin/exams/{a_id}/attempts/{ayu_id}/delete", teacher)

        with step("11 pictures of the editor, its forms and History"):
            pictures("editor", f"/admin/exams/{b_id}/edit", teacher)
            pictures("question-choice", f"/admin/exams/{b_id}/questions/1", teacher)
            pictures("question-fill-in", f"/admin/exams/{b_id}/questions/2", teacher)
            pictures("new-exam", "/admin/exams/new", teacher)
            pictures("settings", f"/admin/exams/{b_id}/settings", teacher)
            pictures("section", f"/admin/exams/{b_id}/sections/1", teacher)
            pictures("history", f"/admin/exams/{b_id}/revisions", teacher)
            pictures("revision-1", f"/admin/exams/{b_id}/revisions/1", teacher)

        with step("12 pictures of the confirmation pages and the Teachers page"):
            pictures("confirm-remove-question", f"/admin/exams/{b_id}/questions/4/remove", teacher)
            pictures("confirm-remove-section", f"/admin/exams/{b_id}/sections/2/remove", teacher)
            pictures("confirm-remove-teacher", f"/admin/teachers/{teacher_id}/remove", teacher)
            pictures("confirm-archive", f"/admin/exams/{a_id}/archive", teacher)
            pictures("teachers", "/admin/teachers", teacher)

        with step("13 no console errors but the 404 page's, no page errors, no foreign requests"):
            expected = f"{base_url}{MISSING}: Failed to load resource: the server responded with a status of 404 (Not Found)"
            problems, missing_errors = [], 0
            for log in logs:
                for e in log["console_errors"]:
                    if e == expected:
                        missing_errors += 1
                    else:
                        problems.append(f"console error: {e}")
                problems += [f"page error: {e}" for e in log["page_errors"]]
                problems += [f"foreign request: {u}" for u in foreign_requests(log["requests"], base_url, oidc_url)]
            if missing_errors != missing_loads:
                problems.append(f"the 404 page logged {missing_errors} console errors in {missing_loads} loads, want one per load")
            if problems:
                raise AssertionError("\n  ".join(["problems found:"] + problems))

        browser.close()
    return overflows


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True)
    add_sign_in_args(parser)
    parser.add_argument("--out", type=Path, default=Path(__file__).parent / "out" / "screenshots")
    parser.add_argument("--readme", type=Path, help="also write the README pictures into this directory")
    args = parser.parse_args()
    base_url, oidc_url = args.base_url.rstrip("/"), args.oidc_url.rstrip("/")
    require_cross_site(base_url, oidc_url)
    try:
        overflows = run(base_url, args.admin_email, oidc_url, args.out, args.readme)
    except StepFailed as err:
        print(err, file=sys.stderr)
        return 1
    if overflows:
        print("FAIL: these pages scroll sideways:\n  " + "\n  ".join(overflows), file=sys.stderr)
        return 1
    print(f"OK: screenshots in {args.out}; no page scrolls sideways")
    return 0


if __name__ == "__main__":
    sys.exit(main())
