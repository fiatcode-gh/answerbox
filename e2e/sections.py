# /// script
# requires-python = ">=3.12"
# dependencies = ["playwright==1.63.0"]
# ///
"""Browser check for exam sections in real Chromium: an imported exam with two
sections shows their headings, instructions and image on the teacher's pages,
the download imports again with the same sections, the editor starts, edits and
removes a section and moves a question across a boundary (a refused title and
an outdated form keep the typed text), every change is a revision, Copy keeps
the sections, students see the headings with the questions shuffled inside
each section, the review shows the headings, the results list and the CSV have
one column per section, the attempt page shows the headings, and an exam
without sections has none. Needs a fresh instance.

With --rollback it checks, against the previous binary (293ab19) on a copy of
the data directory a normal run left, that the sectioned exam still runs there
without its headings, that results and an exam without sections still work,
and that its editor saves the copy (the proof of what that does to the
sections is a query on the data afterwards).

uv run e2e/sections.py --base-url URL --admin-email EMAIL --oidc-url URL --out DIR [--rollback]
"""

import argparse
import csv
import io
import re
import sys
import tempfile
import zipfile
from contextlib import contextmanager
from pathlib import Path

from playwright.sync_api import Locator, Page, expect, sync_playwright

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
    write_png,
)

DESKTOP = {"viewport": {"width": 1280, "height": 800}}
PHONE_VIEWPORT = {"width": 412, "height": 915}
TITLE = "Sections check"
REIMPORT_TITLE = "Sections reimport"
COPY_TITLE = "Sections copy"
PLAIN_TITLE = "Plain check"
SECTION_A = "Bagian A: Pilihan Ganda"
SECTION_B = "Bagian B: Soal Cerita"
SECTION_A_TEXT = "Pilih satu jawaban."
SECTION_B_TEXT = "Baca setiap soal dengan teliti."
STALE = "This exam changed since you opened this page. Nothing was saved."
AYU, BUDI, EKA, FAJAR = "Ayu Lestari", "Budi Santoso", "Eka Putra", "Fajar Nugroho"
SCORE = re.compile(r"^\d+ / \d+$")
# The six questions in file order: (text, options with "*" on the correct one).
# Questions 1 to 3 are in section A, 4 to 6 in section B.
QUESTIONS = [
    ("Berapakah hasil 2 + 3?", ["4", "5 *", "6", "7"]),
    ("Apa ibu kota Indonesia?", ["Bandung", "Jakarta *", "Surabaya", "Medan"]),
    ("Berapa jumlah sisi segitiga?", ["3 *", "4", "5", "6"]),
    ("Andi membeli 4 buku seharga 5.000 rupiah per buku. Berapa yang ia bayar?", ["15.000", "20.000 *", "25.000", "9.000"]),
    ("Kereta berangkat pukul 08.00 dan tiba pukul 10.30. Berapa lama perjalanannya?", ["2 jam", "2,5 jam *", "3 jam", "3,5 jam"]),
    ("Siti punya 12 kelereng dan memberi 5 kepada temannya. Berapa sisa kelerengnya?", ["5", "6", "7 *", "8"]),
]
KEY = [(text, next(o.removesuffix(" *") for o in options if o.endswith(" *"))) for text, options in QUESTIONS]


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


def build_sections_zip(dir: Path) -> Path:
    """Write the two-section exam (exam.md + triangle.png, 160 px wide) as a zip into dir."""
    lines = [
        f"# {TITLE}",
        "shuffle: questions, options",
        "show_result: review",
        "monitor: off",
        "",
        f"## {SECTION_A}",
        "Pilih **satu** jawaban.",
        "![segitiga](triangle.png)",
        "",
    ]
    for n, (text, options) in enumerate(QUESTIONS, start=1):
        if n == 4:
            lines += [f"## {SECTION_B}", SECTION_B_TEXT, ""]
        lines.append(f"{n}. {text}")
        lines += [f"- {option}" for option in options]
        lines.append("")
    image = write_png(dir / "triangle.png", 160, 120)
    path = dir / "sections.zip"
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as z:
        z.writestr("exam.md", "\n".join(lines))
        z.write(image, "triangle.png")
    return path


def exam_page(page: Page, base_url: str, exam_id: int) -> None:
    page.goto(f"{base_url}/admin/exams/{exam_id}")
    expect(page.locator("main.exam-detail")).to_be_visible()


def exams_list(page: Page, base_url: str) -> None:
    page.goto(f"{base_url}/admin/exams")
    expect(page.locator("main h1")).to_have_text("Exams")


def row_by_title(page: Page, title: str) -> Locator:
    return page.locator("#exams tbody tr[data-exam-id]").filter(has=page.get_by_role("cell", name=title, exact=True))


def text_of(scope: Locator) -> str:
    """inner_text with runs of white space made one space."""
    return " ".join(scope.inner_text().split())


def headings(page: Page) -> list[str]:
    """The titles of the .section-head blocks on the page, in order."""
    return [" ".join(t.split()) for t in page.locator(".section-head .section-title").all_inner_texts()]


def heading_before(page: Page, selector: str) -> str | None:
    """The id or, without one, the title of the .section-head directly before selector; None if there is none."""
    return page.locator(selector).evaluate(
        """el => {
            const prev = el.previousElementSibling;
            if (!prev || !prev.classList.contains("section-head")) return null;
            return prev.id || prev.querySelector(".section-title").textContent.trim();
        }"""
    )


def want_heads_before(page: Page, want: dict[str, str]) -> None:
    """Each selector in want has the heading (title or id) directly before it."""
    for selector, title in want.items():
        if (got := heading_before(page, selector)) != title:
            raise AssertionError(f"before {selector}: heading {got!r}, want {title!r}")


def loaded_width(image: Locator) -> int:
    """naturalWidth of image, after it has loaded."""
    image.evaluate(
        "img => img.complete ? null : new Promise((ok, fail) => { img.onload = ok; img.onerror = fail; })"
    )
    return image.evaluate("img => img.naturalWidth")


def want_width(scope: Locator, width: int) -> None:
    images = scope.locator("img")
    expect(images).to_have_count(1)
    if (got := loaded_width(images.first)) != width:
        raise AssertionError(f"image is {got} px wide, want {width}")


def preview(page: Page) -> list[tuple[str, str]]:
    """(question text, correct option text) of every previewed question, in order."""
    questions = page.locator("section.preview-question")
    result = []
    for i in range(questions.count()):
        q = questions.nth(i)
        expect(q.locator(".correct-mark")).to_have_count(1)
        result.append((text_of(q.locator(".question-body")), text_of(q.locator("li[data-correct=true] .option-body"))))
    return result


def sections_shown(page: Page) -> dict:
    """What the teacher's exam page shows of the sections: titles, instructions, image width, questions."""
    bodies = page.locator(".section-head .section-body")
    want_width(bodies.first, 160)
    return {
        "titles": headings(page),
        "bodies": [text_of(bodies.nth(i)) for i in range(bodies.count())],
        "questions": preview(page),
    }


def want_sections_shown(page: Page) -> dict:
    """The exam page shows the two imported sections over questions 1 and 4 and the six questions."""
    want_heads_before(page, {"#q1": SECTION_A, "#q4": SECTION_B})
    expect(page.locator("section.preview-question")).to_have_count(6)
    shown = sections_shown(page)
    want = {"titles": [SECTION_A, SECTION_B], "bodies": [SECTION_A_TEXT, SECTION_B_TEXT], "questions": KEY}
    if shown != want:
        raise AssertionError(f"exam page shows {shown}, want {want}")
    return shown


def history(page: Page, base_url: str, exam_id: int) -> list[list[str]]:
    """Open the exam's page, press History; returns the rows' cells (number, saved, teacher, summary)."""
    exam_page(page, base_url, exam_id)
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


def editor(page: Page, base_url: str, exam_id: int) -> None:
    page.goto(f"{base_url}/admin/exams/{exam_id}/edit")
    expect(page.locator(".editor-page")).to_be_visible()


def press_in_editor(page: Page, exam_id: int, selector: str) -> None:
    """Press a button on the editor page; lands on the editor page again."""
    page.locator(selector).click()
    expect(page).to_have_url(re.compile(rf"/admin/exams/{exam_id}/edit$"))


def rename(page: Page, base_url: str, exam_id: int, title: str) -> None:
    """Edit questions → Settings on the exam's page: save the title."""
    exam_page(page, base_url, exam_id)
    page.locator("#edit-link").click()
    expect(page).to_have_url(re.compile(rf"/admin/exams/{exam_id}/edit$"))
    page.locator("#settings-link").click()
    expect(page).to_have_url(re.compile(rf"/admin/exams/{exam_id}/settings$"))
    page.locator("#settings-form #exam-title").fill(title)
    press_in_editor(page, exam_id, "#settings-form button[type=submit]")
    exam_page(page, base_url, exam_id)
    expect(page.locator(".exam-head h1")).to_have_text(title)


def download(page: Page, base_url: str, exam_id: int, dir: Path) -> Path:
    """Press Download on the exam's page; returns the saved file, named as the server suggested."""
    exam_page(page, base_url, exam_id)
    with page.expect_download() as info:
        page.locator("#download-link").click()
    path = dir / info.value.suggested_filename
    info.value.save_as(path)
    return path


def answer(page: Page, picks: dict[str, bool]) -> None:
    """For each question text, pick its correct option (True) or another one (False); wait until saved."""
    key = dict(KEY)
    questions = page.locator("section.question")
    for i in range(questions.count()):
        q = questions.nth(i)
        text = text_of(q.locator(".question-body"))
        if text not in picks:
            continue
        labels = q.locator("label.option")
        for j in range(labels.count()):
            if (text_of(labels.nth(j).locator(".option-body")) == key[text]) == picks[text]:
                labels.nth(j).click()
                break
        else:
            raise AssertionError(f"question {text!r}: no option to pick")
        expect(q.locator(".q-state")).to_have_attribute("data-state", "saved")
    expect(page.locator("#save-state")).to_have_attribute("data-state", "saved")


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


def student_layout(page: Page) -> list[str]:
    """The children of .exam-questions as 'H:<title>' for a heading and the question text for a question."""
    return page.locator(".exam-questions").evaluate(
        """el => Array.from(el.children)
            .filter(c => c.matches(".section-head, section.question"))
            .map(c => c.matches(".section-head")
                ? "H:" + c.querySelector(".section-title").textContent.trim()
                : c.querySelector(".question-body").innerText.trim().split(/\\s+/).join(" "))"""
    )


def attempt_rows(page: Page, base_url: str, exam_id: int) -> Locator:
    page.goto(f"{base_url}/admin/exams/{exam_id}/attempts")
    return page.locator("#attempts tbody tr[data-attempt-id]")


def attempt_row(page: Page, base_url: str, exam_id: int, name: str) -> Locator:
    return attempt_rows(page, base_url, exam_id).filter(has=page.get_by_role("cell", name=name, exact=True))


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

        with step("2 import the two-section exam: the exam page shows both headings, instructions and the image"):
            exam_id = import_exam(a, build_sections_zip(tmp))
            shown = want_sections_shown(a)
            shot(a, "desktop-exam.png")

        with step("3 the download imports again with the same sections; rename it Sections reimport"):
            (tmp / "downloads").mkdir()
            exam_page(a, base_url, exam_id)
            code = a.locator("#exam-code").inner_text().strip()
            zip_path = download(a, base_url, exam_id, tmp / "downloads")
            if zip_path.name != f"exam-{code}.zip":
                raise AssertionError(f"download named {zip_path.name!r}, want exam-{code}.zip")
            exams_list(a, base_url)
            again_id = import_exam(a, zip_path)
            want_heads_before(a, {"#q1": SECTION_A, "#q4": SECTION_B})
            if (got := sections_shown(a)) != shown:
                raise AssertionError(f"re-imported exam shows {got}, want {shown}")
            rename(a, base_url, again_id, REIMPORT_TITLE)

        with step("4 Start a section at question 2: an empty title is refused with the text kept, then saved"):
            editor(a, base_url, exam_id)
            expect(a.locator("#s1")).to_be_visible()
            expect(a.locator("#s2")).to_be_visible()
            a.locator("#q2 a.start-section").click()
            expect(a).to_have_url(re.compile(rf"/admin/exams/{exam_id}/questions/2/section$"))
            expect(a.locator("h1")).to_have_text("New section at question 2")
            a.locator("#section-title").fill("   ")
            a.locator("#section-text").fill("Sisipan.")
            with a.expect_response(lambda r: r.request.method == "POST" and r.url.endswith("/questions/2/section")) as info:
                a.locator("#save").click()
            if info.value.status != 422:
                raise AssertionError(f"empty section title answered {info.value.status}, want 422")
            expect(a.locator("[role=alert]")).to_have_text("Not saved: section title is empty")
            expect(a.locator("#section-text")).to_have_value("Sisipan.")
            a.locator("#section-title").fill("Bagian Tengah")
            a.locator("#save").click()
            expect(a).to_have_url(re.compile(rf"/admin/exams/{exam_id}/edit#q2$"))
            expect(a.locator(".section-head")).to_have_count(3)
            latest(a, base_url, exam_id, "Section 2 started at question 2", 2)

        with step("5 a section form from an outdated page is refused with 409 and keeps the typed title"):
            editor(a, base_url, exam_id)
            a.locator("#s2 a.edit-section").click()
            expect(a).to_have_url(re.compile(rf"/admin/exams/{exam_id}/sections/2$"))
            expect(a.locator("h1")).to_have_text("Section 2")
            b = a.context.new_page()
            b.goto(f"{base_url}/admin/exams/{exam_id}/sections/2")
            b.locator("#section-title").fill("Bagian A2")
            b.locator("#save").click()
            expect(b).to_have_url(re.compile(rf"/admin/exams/{exam_id}/edit#s2$"))
            latest(b, base_url, exam_id, "Section 2 edited", 3)
            b.close()
            a.bring_to_front()
            a.locator("#section-title").fill("Lama")
            with a.expect_response(lambda r: r.request.method == "POST" and r.url.endswith("/sections/2")) as info:
                a.locator("#save").click()
            if info.value.status != 409:
                raise AssertionError(f"outdated section form answered {info.value.status}, want 409")
            expect(a.locator("[role=alert]")).to_have_text(STALE)
            expect(a.locator("#section-title")).to_have_value("Lama")
            shot(a, "desktop-stale.png")
            if len(history(a, base_url, exam_id)) != 3:
                raise AssertionError("the outdated save changed the history")

        with step("6 Move up on question 2 moves it into section 1; question 3 is section 2's only question"):
            editor(a, base_url, exam_id)
            press_in_editor(a, exam_id, "#q2 form.move-up button")
            latest(a, base_url, exam_id, "Question 2 moved to section 1", 4)
            editor(a, base_url, exam_id)
            want_heads_before(a, {"#q3": "s2"})
            for selector in ("form.move-up", "form.move-down", "a.remove-question"):
                expect(a.locator(f"#q3 {selector}")).to_have_count(0)
            shot(a, "desktop-editor.png")

        with step("7 Remove section 2: two headings again; editor and section form fit a phone"):
            a.locator("#s2 a.remove-section").click()
            confirm(a, "remove-section-form")
            expect(a).to_have_url(re.compile(rf"/admin/exams/{exam_id}/edit$"))
            latest(a, base_url, exam_id, "Section 2 removed", 5)
            editor(a, base_url, exam_id)
            expect(a.locator(".section-head")).to_have_count(2)
            want_heads_before(a, {"#q1": "s1", "#q4": "s2"})
            a.set_viewport_size(PHONE_VIEWPORT)
            no_page_overflow(a)
            shot(a, "phone-editor.png")
            a.goto(f"{base_url}/admin/exams/{exam_id}/sections/1")
            expect(a.locator("main.section-page")).to_be_visible()
            no_page_overflow(a)
            shot(a, "phone-section-form.png")
            a.set_viewport_size(DESKTOP["viewport"])

        with step("8 revision 2 shows three headings, revision 1 two"):
            for n, count in [(2, 3), (1, 2)]:
                a.goto(f"{base_url}/admin/exams/{exam_id}/revisions/{n}")
                expect(a.locator("h1")).to_have_text(f"Revision {n}")
                expect(a.locator(".section-head")).to_have_count(count)

        with step("9 Copy exam keeps the headings and the image; rename it Sections copy"):
            exam_page(a, base_url, exam_id)
            a.locator("#copy-form button[type=submit]").click()
            expect(a).to_have_url(re.compile(r"/admin/exams/\d+$"))
            copy_id = int(re.search(r"/admin/exams/(\d+)$", a.url).group(1))
            if copy_id == exam_id:
                raise AssertionError("Copy exam stayed on the source exam")
            want_heads_before(a, {"#q1": SECTION_A, "#q4": SECTION_B})
            want_width(a.locator(".section-head").first, 160)
            rows = history(a, base_url, copy_id)
            if len(rows) != 1 or not rows[0][3].startswith(f"Copied from {TITLE} ("):
                raise AssertionError(f"copy history {rows}, want one row 'Copied from {TITLE} (…'")
            rename(a, base_url, copy_id, COPY_TITLE)

        ayu = new_context(**phone).new_page()
        with step("10 Ayu sees the headings with each section shuffled on its own; scores 4 / 6; the review has the headings"):
            exam_page(a, base_url, exam_id)
            open_exam(a, exam_id)
            code = exam_code(a, exam_id)
            start(ayu, base_url, code, AYU)
            got = student_layout(ayu)
            texts = [text for text, _ in QUESTIONS]
            if (
                len(got) != 8
                or got[0] != f"H:{SECTION_A}"
                or got[4] != f"H:{SECTION_B}"
                or sorted(got[1:4]) != sorted(texts[:3])
                or sorted(got[5:8]) != sorted(texts[3:])
            ):
                raise AssertionError(f"exam page shows {got}, want heading A, questions 1 to 3, heading B, questions 4 to 6")
            eyebrows = [e.strip() for e in ayu.locator("section.question .eyebrow").all_inner_texts()]
            if eyebrows != [f"Question {n} of 6" for n in range(1, 7)]:
                raise AssertionError(f"eyebrows {eyebrows}")
            bubbles = [b.strip() for b in ayu.locator("#question-nav .bubble").all_inner_texts()]
            if bubbles != [str(n) for n in range(1, 7)]:
                raise AssertionError(f"navigator bubbles {bubbles}")
            bodies = ayu.locator(".exam-questions .section-head .section-body")
            expect(bodies).to_have_count(2)
            if (got := [text_of(bodies.nth(i)) for i in range(2)]) != [SECTION_A_TEXT, SECTION_B_TEXT]:
                raise AssertionError(f"instructions {got}")
            want_width(bodies.first, 160)
            no_page_overflow(ayu)
            shot(ayu, "phone-exam.png")
            answer(ayu, {text: n <= 4 for n, text in enumerate(texts, start=1)})
            if (score := submit(ayu)) != "4 / 6":
                raise AssertionError(f"Ayu scored {score}, want 4 / 6")
            review = ayu.locator(".review").evaluate(
                """el => Array.from(el.children)
                    .filter(c => c.matches(".section-head, section.review-question"))
                    .map(c => c.matches(".section-head") ? "H:" + c.querySelector(".section-title").textContent.trim() : "Q")"""
            )
            if review != [f"H:{SECTION_A}", "Q", "Q", "Q", f"H:{SECTION_B}", "Q", "Q", "Q"]:
                raise AssertionError(f"review shows {review}")
            expect(ayu.locator(".section-score")).to_have_count(0)
            shot(ayu, "phone-review.png")

        with step("11 Budi scores 3 / 6"):
            budi = new_context(**phone).new_page()
            start(budi, base_url, code, BUDI)
            answer(budi, {text: n > 3 for n, (text, _) in enumerate(QUESTIONS, start=1)})
            if (score := submit(budi)) != "3 / 6":
                raise AssertionError(f"Budi scored {score}, want 3 / 6")

        with step("12 the results list and the CSV have one column per section"):
            rows = attempt_rows(a, base_url, exam_id)
            expect(rows).to_have_count(2)
            header = [" ".join(h.split()) for h in a.locator("#attempts thead th").all_inner_texts()]
            i = header.index("Score")
            if header[i : i + 4] != ["Score", SECTION_A, SECTION_B, "Time left"]:
                raise AssertionError(f"results header {header}")
            for name, want in [(AYU, ["4 / 6", "3 / 3", "1 / 3"]), (BUDI, ["3 / 6", "0 / 3", "3 / 3"])]:
                found = rows.filter(has=a.get_by_role("cell", name=name, exact=True))
                cells = [c.strip() for c in found.locator("td").all_inner_texts()]
                if cells[i : i + 3] != want:
                    raise AssertionError(f"{name}'s row {cells}, want {want} from the Score column")
            shot(a, "desktop-results.png")
            response = a.context.request.get(f"{base_url}/admin/exams/{exam_id}/attempts.csv")
            if response.status != 200:
                raise AssertionError(f"CSV answered {response.status}")
            body = response.text()
            first = body.split("\r\n", 1)[0].split("\n", 1)[0]
            if first != f"name,status,score,total,{SECTION_A},{SECTION_B},started,finished,flags":
                raise AssertionError(f"CSV header {first!r}")
            records = {r[0]: r for r in csv.reader(io.StringIO(body))}
            for name, want in [(AYU, ["4", "6", "3", "1"]), (BUDI, ["3", "6", "0", "3"])]:
                if records.get(name, [])[2:6] != want:
                    raise AssertionError(f"CSV row of {name} has {records.get(name, [])[2:6]}, want {want}")

        with step("13 Ayu's attempt page shows the headings before questions 1 and 4"):
            href = attempt_row(a, base_url, exam_id, AYU).get_by_role("link", name="View", exact=True).get_attribute("href")
            a.goto(base_url + href)
            expect(a.locator("main.attempt-detail h1")).to_have_text(AYU)
            expect(a.locator(".section-head")).to_have_count(2)
            want_heads_before(a, {"#q1": SECTION_A, "#q4": SECTION_B})

        with step("14 an exam without sections shows no heading and offers Start a section on question 1 only"):
            exams_list(a, base_url)
            (tmp / "plain").mkdir()
            plain_zip, _ = build_fixture_zip(tmp / "plain", title=PLAIN_TITLE, monitor="off")
            plain_id = import_exam(a, plain_zip)
            open_exam(a, plain_id)
            exam_page(a, base_url, plain_id)
            expect(a.locator("section.preview-question")).to_have_count(6)
            expect(a.locator(".section-head")).to_have_count(0)
            editor(a, base_url, plain_id)
            expect(a.locator("a.start-section")).to_have_count(1)
            expect(a.locator("#q1 a.start-section")).to_have_count(1)

        with step("15 no page errors or foreign requests; console errors are the 422 and the 409 page loads"):
            check_logs(
                logs,
                base_url,
                oidc_url,
                [
                    console_error(f"{base_url}/admin/exams/{exam_id}/questions/2/section", 422, "Unprocessable Entity"),
                    console_error(f"{base_url}/admin/exams/{exam_id}/sections/2", 409, "Conflict"),
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

        def shot(page: Page, name: str) -> None:
            page.screenshot(path=out / name, full_page=True, animations="disabled")

        a = new_context(**DESKTOP).new_page()
        with step("R1 admin signs in"):
            teacher_login(a, base_url, admin_email, "Ada Admin")

        with step("R2 the list has Sections check open, Sections copy closed and Plain check open"):
            ids, codes = {}, {}
            for title, badge in [(TITLE, "Open"), (COPY_TITLE, "Closed"), (PLAIN_TITLE, "Open")]:
                found = row_by_title(a, title)
                expect(found).to_have_count(1)
                expect(found.locator(".badge")).to_have_text(badge)
                ids[title] = int(found.get_attribute("data-exam-id"))
                codes[title] = exam_code(a, ids[title])
            shot(a, "desktop-exams.png")

        with step("R3 Sections check previews six questions without headings; its results have Ayu and Budi"):
            exam_id = ids[TITLE]
            exam_page(a, base_url, exam_id)
            expect(a.locator("section.preview-question")).to_have_count(6)
            expect(a.locator(".section-head")).to_have_count(0)
            expect(a.locator("main")).not_to_contain_text("Bagian A")
            shot(a, "desktop-exam.png")
            for name, score in [(AYU, "4 / 6"), (BUDI, "3 / 6")]:
                found = attempt_row(a, base_url, exam_id, name)
                expect(found.locator(".badge")).to_have_text("Submitted")
                expect(found).to_contain_text(score)
            expect(a.locator("#attempts thead th")).to_have_count(9)
            shot(a, "desktop-results.png")

        with step("R4 Eka takes Sections check: six questions, no section on the page; the results list has her"):
            eka = new_context(**phone).new_page()
            start(eka, base_url, codes[TITLE], EKA)
            expect(eka.locator("section.question")).to_have_count(6)
            expect(eka.locator("body")).not_to_contain_text("Bagian")
            shot(eka, "phone-exam.png")
            answer_one(eka)
            eka_score = submit(eka)
            found = attempt_row(a, base_url, exam_id, EKA)
            expect(found.locator(".badge")).to_have_text("Submitted")
            expect(found).to_contain_text(eka_score)

        with step("R5 Fajar takes Plain check; its results list has him"):
            fajar = new_context(**phone).new_page()
            start(fajar, base_url, codes[PLAIN_TITLE], FAJAR)
            answer_one(fajar)
            fajar_score = submit(fajar)
            found = attempt_row(a, base_url, ids[PLAIN_TITLE], FAJAR)
            expect(found.locator(".badge")).to_have_text("Submitted")
            expect(found).to_contain_text(fajar_score)

        with step("R6 the editor of Sections copy shows six questions without headings and saves question 1"):
            copy_id = ids[COPY_TITLE]
            editor(a, base_url, copy_id)
            expect(a.locator("section.preview-question")).to_have_count(6)
            expect(a.locator(".section-head")).to_have_count(0)
            a.locator("#q1 a.edit-question").click()
            expect(a).to_have_url(re.compile(rf"/admin/exams/{copy_id}/questions/1$"))
            text = a.locator("#question-text")
            text.fill(text.input_value().rstrip("\n") + " (edited)")
            a.locator("#save").click()
            expect(a).to_have_url(re.compile(rf"/admin/exams/{copy_id}/edit#q1$"))
            latest(a, base_url, copy_id, "Question 1 edited", 3)

        with step("R7 no page errors, foreign requests or console errors"):
            check_logs(logs, base_url, oidc_url, [])

        browser.close()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", required=True)
    add_sign_in_args(parser)
    parser.add_argument("--out", type=Path, default=Path(__file__).parent / "out" / "sections")
    parser.add_argument("--rollback", action="store_true", help="check the previous binary on the data a normal run left")
    args = parser.parse_args()
    base_url, oidc_url = args.base_url.rstrip("/"), args.oidc_url.rstrip("/")
    require_cross_site(base_url, oidc_url)
    try:
        (run_rollback if args.rollback else run)(base_url, args.admin_email, oidc_url, args.out)
    except StepFailed as err:
        print(err, file=sys.stderr)
        return 1
    mode = "rollback check" if args.rollback else "sections check"
    print(f"OK: {mode} passed; screenshots in {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
