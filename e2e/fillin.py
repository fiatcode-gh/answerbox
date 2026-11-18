# /// script
# requires-python = ">=3.12"
# dependencies = ["playwright==1.63.0"]
# ///
"""Browser check for fill-in questions in real Chromium: the new-exam form
makes question 1 fill-in before the exam exists, the editor adds a fill-in
question (an empty accepted answer is refused with the typed text kept) and
edits its answers (one revision; an abandoned kind switch adds none), the exam
page, editor page and revision view show the accepted answers, Download imports
again and Copy keeps them, a student types answers (saved after a pause,
cleared, kept over a reload, retried offline with Submit held back, a paste
and a drop blocked and flagged) and is scored by the matching rule, the review shows the
typed and the accepted answers, the teacher marks a typed answer right or wrong
and removes the mark, and every score (attempt page, results list, section
columns, CSV, the student's result page) follows. Needs a fresh instance; it
leaves Ayu's question 3 marked wrong and Budi's question 1 marked right, and
saves the CSV of Fill-in check as <out>/attempts.csv.

With --rollback it checks the previous binary (1bddec5) on a copy of the data
directory a normal run left. Observed there:
- the exams list works; Plain check (multiple choice) is taken by a new
  student, who is scored, and her row in the results list and the CSV has that
  score;
- the results of Fill-in check count only the multiple-choice questions: Ayu
  2 / 4 (sections 1 / 2 and 1 / 2), Budi 1 / 4 (1 / 2 and 0 / 2), Citra 0 / 4;
- Ayu's attempt page shows questions 1 and 3 with "No answer" and no option;
- Citra, resumed with her resume code, sees questions 1 and 3 without an
  answer box or option;
- on Fill-in copy (no attempts) saving question 1 in the editor is refused
  with 422 "Not saved: question 1 needs at least two options"; Download gives
  an exam.md without "=" lines; Copy exam answers the 500 page;
- no attempt is deleted and Replace is not pressed.

With --returned --expect-csv FILE (this release again, on the directory the
rollback run used) it checks that the CSV of Fill-in check is byte-equal to
FILE and that both overrides are still shown.

uv run e2e/fillin.py --base-url URL --admin-email EMAIL --oidc-url URL --out DIR [--rollback | --returned --expect-csv FILE]
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

from playwright.sync_api import Locator, Page, TimeoutError as PlaywrightTimeout, expect, sync_playwright

from common import (
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
TITLE = "Fill-in check"
REIMPORT_TITLE = "Fill-in reimport"
COPY_TITLE = "Fill-in copy"
PLAIN_TITLE = "Plain check"
SECTION_A, SECTION_B = "Bagian A", "Bagian B"
AYU, BUDI, CITRA, DEWI = "Ayu Lestari", "Budi Santoso", "Citra Dewi", "Dewi Anggraini"
SCORE = re.compile(r"^\d+ / \d+$")
# The four questions in file order as the teacher's pages show them: the
# accepted answers of a fill-in question, (option, correct) of the others.
Q1_TEXT, Q3_TEXT = "Ibu kota Indonesia adalah ....", "Berapakah 1.000 + 3?"
Q2_TEXT, Q4_TEXT = "Berapakah hasil 2 + 3?", "Berapa jumlah sisi segitiga?"
Q2_OPTIONS, Q4_OPTIONS = ["4", "5", "6", "7"], ["3", "4", "5", "6"]
Q2_RIGHT, Q4_RIGHT = 1, 0
CONTENT = {
    "heads": [SECTION_A, SECTION_B],
    "questions": [
        (Q1_TEXT, "answers", ["Jakarta", "DKI Jakarta"]),
        (Q2_TEXT, "options", [(o, i == Q2_RIGHT) for i, o in enumerate(Q2_OPTIONS)]),
        (Q3_TEXT, "answers", ["1.003", "1003 buah"]),
        (Q4_TEXT, "options", [(o, i == Q4_RIGHT) for i, o in enumerate(Q4_OPTIONS)]),
    ],
}
REFUSED_ANSWER = "Not saved: question 3, accepted answer 2 has no text"
OLD_REFUSED = "Not saved: question 1 needs at least two options"
OVERRIDE = re.compile(r"^Marked (right|wrong) by admin@example\.test on \S.*\d$")
# The browser reports each request it could not send while offline; the run
# goes offline on purpose, so these are not page errors.
OFFLINE = "Failed to load resource: net::ERR_INTERNET_DISCONNECTED"


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
    """No page errors or foreign requests; the console errors other than the
    offline reports are exactly expected, in order."""
    problems, errors = [], []
    for log in logs:
        errors += [e for e in log["console_errors"] if not e.endswith(OFFLINE)]
        problems += [f"page error: {e}" for e in log["page_errors"]]
        problems += [f"foreign request: {u}" for u in foreign_requests(log["requests"], base_url, oidc_url)]
    if errors != expected:
        problems.append("console errors:\n    " + "\n    ".join(errors) + "\n  want:\n    " + "\n    ".join(expected))
    if problems:
        raise AssertionError("\n  ".join(["problems found:"] + problems))


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


def texts_of(scope: Locator) -> list[str]:
    return [" ".join(t.split()) for t in scope.all_inner_texts()]


def content(page: Page) -> dict:
    """The section titles and the questions a teacher's page shows: (text,
    "answers", accepted answers) or (text, "options", [(option, correct)])."""
    questions = page.locator("section.preview-question")
    shown = []
    for i in range(questions.count()):
        q = questions.nth(i)
        text = text_of(q.locator(".question-body"))
        if q.locator(".accepted-answers").count():
            expect(q.locator(".preview-options")).to_have_count(0)
            shown.append((text, "answers", texts_of(q.locator(".accepted-answers li"))))
            continue
        items = q.locator(".preview-options li")
        shown.append(
            (
                text,
                "options",
                [(text_of(items.nth(j).locator(".option-body")), items.nth(j).get_attribute("data-correct") == "true") for j in range(items.count())],
            )
        )
    return {"heads": texts_of(page.locator(".section-head .section-title")), "questions": shown}


def want_content(page: Page, where: str) -> None:
    if (got := content(page)) != CONTENT:
        raise AssertionError(f"{where} shows {got}, want {CONTENT}")


def history(page: Page, base_url: str, exam_id: int) -> list[list[str]]:
    """Open the exam's page, press History; returns the rows' cells (number, saved, teacher, summary)."""
    exam_page(page, base_url, exam_id)
    page.locator("#history-link").click()
    expect(page).to_have_url(re.compile(rf"/admin/exams/{exam_id}/revisions$"))
    expect(page.locator("#revisions")).to_be_visible()
    rows = page.locator("#revisions tbody tr[data-revision]")
    return [[c.strip() for c in rows.nth(i).locator("td").all_inner_texts()[:4]] for i in range(rows.count())]


def editor(page: Page, base_url: str, exam_id: int) -> None:
    page.goto(f"{base_url}/admin/exams/{exam_id}/edit")
    expect(page.locator(".editor-page")).to_be_visible()


def press_in_editor(page: Page, exam_id: int, selector: str) -> None:
    """Press a button on the editor page; lands on the editor page again."""
    page.locator(selector).click()
    expect(page).to_have_url(re.compile(rf"/admin/exams/{exam_id}/edit$"))


def rename(page: Page, base_url: str, exam_id: int, title: str) -> None:
    """Editor → Settings: save the title."""
    page.goto(f"{base_url}/admin/exams/{exam_id}/settings")
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


def exam_text(path: Path) -> str:
    """The exam file text of a download: the .md itself or the one .md in a zip."""
    if path.suffix == ".md":
        return path.read_text()
    with zipfile.ZipFile(path) as z:
        return z.read(next(n for n in z.namelist() if n.endswith(".md"))).decode()


def press(page: Page, label: str) -> None:
    """Press a form button by its label; the form re-renders on the same URL."""
    url = page.url
    page.get_by_role("button", name=label, exact=True).click()
    page.wait_for_load_state()
    expect(page).to_have_url(url)


def fill_answers(page: Page, answers: list[str]) -> None:
    """Fill the accepted-answer boxes of a question form, pressing Add accepted answer as needed."""
    boxes = page.locator(".answer-list input[name=answer]")
    while boxes.count() < len(answers):
        press(page, "Add accepted answer")
    for i, text in enumerate(answers):
        boxes.nth(i).fill(text)


def fill_options(page: Page, options: list[str], right: int) -> None:
    boxes = page.locator(".option-list textarea[name=option]")
    expect(boxes).to_have_count(len(options))
    for i, text in enumerate(options):
        boxes.nth(i).fill(text)
    page.locator(f".option-list input[name=correct][value='{right}']").check()


def add_question(page: Page, base_url: str, exam_id: int, text: str, options: list[str], right: int) -> None:
    """Add a multiple-choice question from the editor page."""
    editor(page, base_url, exam_id)
    page.locator("#add-question").click()
    expect(page).to_have_url(re.compile(rf"/admin/exams/{exam_id}/questions/new$"))
    page.locator("#question-text").fill(text)
    fill_options(page, options, right)
    page.locator("#save").click()
    expect(page).to_have_url(re.compile(rf"/admin/exams/{exam_id}/edit#q\d+$"))


def start_section(page: Page, base_url: str, exam_id: int, n: int, title: str) -> None:
    editor(page, base_url, exam_id)
    page.locator(f"#q{n} a.start-section").click()
    expect(page).to_have_url(re.compile(rf"/admin/exams/{exam_id}/questions/{n}/section$"))
    page.locator("#section-title").fill(title)
    page.locator("#save").click()
    expect(page).to_have_url(re.compile(rf"/admin/exams/{exam_id}/edit#q{n}$"))


def box(page: Page, n: int) -> Locator:
    return page.locator(f"section.question#q{n} input.answer-text")


def type_answer(page: Page, n: int, text: str) -> None:
    """Type into question n's box; it is saved once typing pauses."""
    box(page, n).fill(text)


def wait_saved(page: Page, timeout: float = 10_000) -> None:
    expect(page.locator("#save-state")).to_have_attribute("data-state", "saved", timeout=timeout)
    expect(page.locator("#save-state")).to_have_text("✓ All answers saved")


def pick(page: Page, n: int, option: str) -> None:
    """Pick the option with text option in question n; wait until saved."""
    q = page.locator(f"section.question#q{n}")
    labels = q.locator("label.option")
    for j in range(labels.count()):
        if text_of(labels.nth(j).locator(".option-body")) == option:
            labels.nth(j).click()
            break
    else:
        raise AssertionError(f"question {n} has no option {option!r}")
    expect(q.locator(".q-state")).to_have_attribute("data-state", "saved")


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


def review_marks(page: Page) -> list[str]:
    """The mark of each fill-in question in the review, in order."""
    return texts_of(page.locator(".review-fill-in .review-mark"))


# A probe that runs after the page's own handlers (window is the last
# bubbling target) and records whether the page blocked the paste.
PROBE_JS = """() => {
  window.__blocked = {};
  window.addEventListener("paste", e => { window.__blocked.paste = e.defaultPrevented; });
}"""


def paste_into(page: Page, target: Locator) -> bool:
    """Press Ctrl+V in target with text on the clipboard; returns whether the paste was blocked."""
    page.evaluate(PROBE_JS)
    page.evaluate("navigator.clipboard.writeText('Bogor')")
    target.focus()
    page.keyboard.press("Control+V")
    try:
        page.wait_for_function("'paste' in window.__blocked", timeout=2000)
    except PlaywrightTimeout:
        # Some Chromium builds fire no paste event in headless mode; deliver
        # the event the browser would.
        print("  note: Ctrl+V fired no paste event; dispatching one", flush=True)
        target.evaluate("el => el.dispatchEvent(new ClipboardEvent('paste', {bubbles: true, cancelable: true}))")
    return page.evaluate("window.__blocked.paste")


# Drops text on an element; returns whether the page blocked the drop.
DROP_JS = """el => {
  const data = new DataTransfer();
  data.setData("text/plain", "Bogor");
  const event = new DragEvent("drop", {bubbles: true, cancelable: true, dataTransfer: data});
  el.dispatchEvent(event);
  return event.defaultPrevented;
}"""


def attempt_rows(page: Page, base_url: str, exam_id: int, query: str = "") -> Locator:
    page.goto(f"{base_url}/admin/exams/{exam_id}/attempts{query}")
    return page.locator("#attempts tbody tr[data-attempt-id]")


def attempt_row(page: Page, base_url: str, exam_id: int, name: str) -> Locator:
    return attempt_rows(page, base_url, exam_id).filter(has=page.get_by_role("cell", name=name, exact=True))


def row_cells(page: Page, base_url: str, exam_id: int) -> dict[str, list[str]]:
    """Name → the cells from Score on (score and the section scores), from the results list."""
    rows = attempt_rows(page, base_url, exam_id)
    expect(rows.first).to_be_visible()
    header = texts_of(page.locator("#attempts thead th"))
    i = header.index("Score")
    out = {}
    for j in range(rows.count()):
        cells = [c.strip() for c in rows.nth(j).locator("td").all_inner_texts()]
        out[cells[0]] = cells[i : i + 3]
    return out


def attempt_page(page: Page, base_url: str, exam_id: int, name: str) -> None:
    href = attempt_row(page, base_url, exam_id, name).get_by_role("link", name="View", exact=True).get_attribute("href")
    page.goto(base_url + href)
    expect(page.locator("main.attempt-detail h1")).to_have_text(name)


def attempt_score(page: Page) -> str:
    return page.locator(".attempt-summary dd").first.inner_text().strip()


def override(page: Page, n: int, mark: str) -> None:
    """Press Mark right, Mark wrong or Remove override on question n of the attempt page."""
    url = page.url.split("#")[0]
    page.locator(f"#q{n} .override-form button[value={mark}]").click()
    expect(page).to_have_url(re.compile(rf"^{re.escape(url)}#q{n}$"))


def fetch_csv(page: Page, base_url: str, exam_id: int) -> bytes:
    response = page.context.request.get(f"{base_url}/admin/exams/{exam_id}/attempts.csv")
    if response.status != 200:
        raise AssertionError(f"CSV answered {response.status}")
    return response.body()


def csv_records(body: bytes) -> dict[str, list[str]]:
    return {r[0]: r for r in csv.reader(io.StringIO(body.decode()))}


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
        with step("0 admin signs in; the instance is fresh"):
            teacher_login(a, base_url, admin_email, "Ada Admin")
            if a.get_by_text("No exams yet.").count() != 1:
                raise AssertionError("not a fresh instance: the exams page does not say 'No exams yet.'")

        with step("1 New exam: question 1 is made fill-in on the form before the exam exists"):
            a.locator("#new-exam").click()
            expect(a).to_have_url(re.compile(r"/admin/exams/new$"))
            a.locator("#exam-title").fill(TITLE)
            a.locator("#exam-shuffle").select_option("none")
            a.locator("#exam-show-result").select_option("review")
            expect(a.locator("#exam-monitor")).to_be_checked()
            press(a, "Switch to fill-in")
            expect(a.locator(".question-kind")).to_contain_text("Fill-in")
            expect(a.locator("textarea[name=option]").first).to_be_hidden()
            expect(a.locator(".answer-list input[name=answer]").first).to_be_visible()
            expect(a.locator("#exam-title")).to_have_value(TITLE)
            if a.context.request.get(f"{base_url}/admin/exams").text().count("No exams yet.") != 1:
                raise AssertionError("the kind switch created an exam")
            a.locator("#question-text").fill(Q1_TEXT)
            fill_answers(a, ["Jakarta", "DKI Jakarta"])
            shot(a, "desktop-new-exam.png")
            a.locator("#save").click()
            expect(a).to_have_url(re.compile(r"/admin/exams/\d+/edit$"))
            exam_id = int(re.search(r"/admin/exams/(\d+)/edit$", a.url).group(1))

        with step("2 add questions 2 to 4 (an empty accepted answer is refused, text kept); sections A and B"):
            add_question(a, base_url, exam_id, Q2_TEXT, Q2_OPTIONS, Q2_RIGHT)
            editor(a, base_url, exam_id)
            a.locator("#add-question").click()
            press(a, "Switch to fill-in")
            a.locator("#question-text").fill(Q3_TEXT)
            fill_answers(a, ["1.003", ""])
            with a.expect_response(lambda r: r.request.method == "POST" and r.url.endswith("/questions/new")) as info:
                a.locator("#save").click()
            if info.value.status != 422:
                raise AssertionError(f"empty accepted answer answered {info.value.status}, want 422")
            expect(a.locator("[role=alert]")).to_have_text(REFUSED_ANSWER)
            expect(a.locator("#question-text")).to_have_value(Q3_TEXT)
            answers = a.locator(".answer-list input[name=answer]")
            expect(answers).to_have_count(2)
            expect(answers.nth(0)).to_have_value("1.003")
            shot(a, "desktop-refused-answer.png")
            a.locator(".answer-list button[value='answer-remove:1']").click()
            expect(answers).to_have_count(1)
            a.locator("#save").click()
            expect(a).to_have_url(re.compile(rf"/admin/exams/{exam_id}/edit#q3$"))
            add_question(a, base_url, exam_id, Q4_TEXT, Q4_OPTIONS, Q4_RIGHT)
            start_section(a, base_url, exam_id, 1, SECTION_A)
            start_section(a, base_url, exam_id, 3, SECTION_B)

        with step("3 edit question 3: add 1003 buah; an abandoned kind switch adds no revision"):
            editor(a, base_url, exam_id)
            a.locator("#q3 a.edit-question").click()
            expect(a).to_have_url(re.compile(rf"/admin/exams/{exam_id}/questions/3$"))
            fill_answers(a, ["1.003", "1003 buah"])
            a.locator("#save").click()
            expect(a).to_have_url(re.compile(rf"/admin/exams/{exam_id}/edit#q3$"))
            rows = history(a, base_url, exam_id)
            if rows[0][3] != "Question 3 edited; accepted answers changed":
                raise AssertionError(f"newest revision is {rows[0][3]!r}")
            editor(a, base_url, exam_id)
            a.locator("#q3 a.edit-question").click()
            press(a, "Switch to multiple choice")
            expect(a.locator("textarea[name=option]").first).to_be_visible()
            shot(a, "desktop-kind-switch.png")
            editor(a, base_url, exam_id)
            if (again := history(a, base_url, exam_id)) != rows:
                raise AssertionError(f"the abandoned kind switch changed the history: {again[0]} over {rows[0]}")
            revision = int(rows[0][0])

        with step("4 exam page, editor page and the latest revision show the accepted answers and options"):
            exam_page(a, base_url, exam_id)
            want_content(a, "the exam page")
            shot(a, "desktop-exam.png")
            editor(a, base_url, exam_id)
            want_content(a, "the editor page")
            shot(a, "desktop-editor.png")
            a.goto(f"{base_url}/admin/exams/{exam_id}/revisions/{revision}")
            expect(a.locator("h1")).to_have_text(f"Revision {revision}")
            want_content(a, f"revision {revision}")

        with step("5 the download imports again and Copy keeps the content"):
            (tmp / "downloads").mkdir()
            downloaded = download(a, base_url, exam_id, tmp / "downloads")
            md = exam_text(downloaded)
            for line in ["= Jakarta", "= DKI Jakarta", "= 1.003", "= 1003 buah"]:
                if line not in md.splitlines():
                    raise AssertionError(f"exam.md has no line {line!r}:\n{md}")
            exams_list(a, base_url)
            again_id = import_exam(a, downloaded)
            want_content(a, "the re-imported exam")
            rename(a, base_url, again_id, REIMPORT_TITLE)
            exam_page(a, base_url, exam_id)
            a.locator("#copy-form button[type=submit]").click()
            expect(a).to_have_url(re.compile(r"/admin/exams/\d+$"))
            copy_id = int(re.search(r"/admin/exams/(\d+)$", a.url).group(1))
            if copy_id == exam_id:
                raise AssertionError("Copy exam stayed on the source exam")
            want_content(a, "the copy")
            rename(a, base_url, copy_id, COPY_TITLE)

        with step("6 import and open Plain check"):
            exams_list(a, base_url)
            (tmp / "plain").mkdir()
            plain_zip, _ = build_fixture_zip(tmp / "plain", title=PLAIN_TITLE, monitor="off")
            plain_id = import_exam(a, plain_zip)
            open_exam(a, plain_id)

        ayu_context = new_context(**DESKTOP)
        ayu_context.grant_permissions(["clipboard-read", "clipboard-write"], origin=base_url)
        ayu = ayu_context.new_page()
        with step("7a open Fill-in check; Ayu's box saves after a pause, clears, and keeps the clear over a reload"):
            exam_page(a, base_url, exam_id)
            open_exam(a, exam_id)
            code = exam_code(a, exam_id)
            start(ayu, base_url, code, AYU)
            expect(ayu.locator("section.question")).to_have_count(4)
            for n in (2, 4):
                expect(box(ayu, n)).to_have_count(0)
            for n in (1, 3):
                expect(ayu.locator(f"section.question#q{n} input[type=radio]")).to_have_count(0)
            for name, value in [("type", "text"), ("maxlength", "200"), ("autocomplete", "off"), ("autocapitalize", "off"), ("spellcheck", "false")]:
                expect(box(ayu, 1)).to_have_attribute(name, value)
            type_answer(ayu, 1, "jakarta")
            q1 = ayu.locator("section.question#q1 .q-state")
            expect(q1).to_have_text("Saved")
            wait_saved(ayu)
            expect(ayu.locator("#answered-count")).to_have_text("Answered 1 of 4")
            expect(ayu.locator("#question-nav a.bubble[href='#q1']")).to_have_attribute("data-answered", "true")
            shot(ayu, "desktop-typed.png")
            type_answer(ayu, 1, "")
            expect(ayu.locator("#answered-count")).to_have_text("Answered 0 of 4")
            expect(ayu.locator("#question-nav a.bubble[href='#q1']")).to_have_attribute("data-answered", "false")
            expect(q1).to_have_attribute("data-state", "idle")
            wait_saved(ayu)
            ayu.reload()
            expect(box(ayu, 1)).to_have_value("")
            expect(ayu.locator("#answered-count")).to_have_text("Answered 0 of 4")

        with step("7b offline: a typed answer reads Not saved — retrying and holds Submit back; saved back online"):
            type_answer(ayu, 1, "  JAKARTA ")
            wait_saved(ayu)
            ayu_context.set_offline(True)
            type_answer(ayu, 3, "1003")
            expect(ayu.locator("#save-state")).to_have_attribute("data-state", "error", timeout=15_000)
            expect(ayu.locator("#save-state")).to_have_text("✗ Not saved — retrying")
            ayu.locator("#submit-open").click()
            expect(ayu.locator("#submit-dialog")).to_be_visible()
            expect(ayu.locator("#submit-confirm")).to_be_disabled()
            shot(ayu, "desktop-offline.png")
            ayu.locator("#submit-cancel").click()
            ayu_context.set_offline(False)
            wait_saved(ayu, timeout=20_000)
            expect(ayu.locator("section.question#q3 .q-state")).to_have_text("Saved")

        with step("7c picks, a blocked and flagged paste and drop, a reload keeps every answer; Ayu scores 4 / 4"):
            pick(ayu, 2, Q2_OPTIONS[Q2_RIGHT])
            pick(ayu, 4, Q4_OPTIONS[Q4_RIGHT])
            wait_saved(ayu)
            before = box(ayu, 1).input_value()
            with ayu.expect_response(lambda r: r.url.endswith("/exam/event")) as event:
                if not paste_into(ayu, box(ayu, 1)):
                    raise AssertionError("paste into the box was not blocked")
            if event.value.status != 200:
                raise AssertionError(f"the paste event answered {event.value.status}")
            if (after := box(ayu, 1).input_value()) != before:
                raise AssertionError(f"the paste changed the box from {before!r} to {after!r}")
            expect(ayu.locator("#monitor-banner")).to_be_visible()
            shot(ayu, "desktop-paste.png")
            before = box(ayu, 3).input_value()
            with ayu.expect_response(lambda r: r.url.endswith("/exam/event")) as event:
                if not box(ayu, 3).evaluate(DROP_JS):
                    raise AssertionError("a drop into the box was not blocked")
            if event.value.status != 200:
                raise AssertionError(f"the drop event answered {event.value.status}")
            if (after := box(ayu, 3).input_value()) != before:
                raise AssertionError(f"the drop changed the box from {before!r} to {after!r}")
            ayu.reload()
            expect(box(ayu, 1)).to_have_value("JAKARTA")
            expect(box(ayu, 3)).to_have_value("1003")
            expect(ayu.locator("section.question#q2 input[type=radio]:checked")).to_have_count(1)
            expect(ayu.locator("section.question#q4 input[type=radio]:checked")).to_have_count(1)
            expect(ayu.locator("#answered-count")).to_have_text("Answered 4 of 4")
            if (score := submit(ayu)) != "4 / 4":
                raise AssertionError(f"Ayu scored {score}, want 4 / 4")
            if (typed := texts_of(ayu.locator(".review-fill-in .typed-text"))) != ["JAKARTA", "1003"]:
                raise AssertionError(f"review shows typed answers {typed}")
            if (marks := review_marks(ayu)) != ["✓ Correct", "✓ Correct"]:
                raise AssertionError(f"review marks {marks}")
            accepted = ayu.locator(".review-fill-in .accepted-answers")
            if [texts_of(accepted.nth(i).locator("li")) for i in range(2)] != [["Jakarta", "DKI Jakarta"], ["1.003", "1003 buah"]]:
                raise AssertionError("review shows other accepted answers")
            shot(ayu, "desktop-review.png")

        budi = new_context(**phone).new_page()
        with step("8 Budi (phone): Bandung, 1.003,0, question 2 right, question 4 wrong scores 2 / 4"):
            start(budi, base_url, code, BUDI)
            no_page_overflow(budi)
            type_answer(budi, 1, "Bandung")
            type_answer(budi, 3, "1.003,0")
            pick(budi, 2, Q2_OPTIONS[Q2_RIGHT])
            pick(budi, 4, Q4_OPTIONS[Q4_RIGHT + 1])
            wait_saved(budi)
            shot(budi, "phone-exam.png")
            if (score := submit(budi)) != "2 / 4":
                raise AssertionError(f"Budi scored {score}, want 2 / 4")
            if (marks := review_marks(budi)) != ["✗ Not correct", "✓ Correct"]:
                raise AssertionError(f"review marks {marks}")
            no_page_overflow(budi)
            shot(budi, "phone-review.png")

        with step("9 Citra starts and types nothing"):
            citra_context = new_context(**phone)
            citra = citra_context.new_page()
            start(citra, base_url, code, CITRA)
            citra_context.close()

        with step("10 attempt pages: typed and accepted answers, automatic marks, overrides"):
            attempt_page(a, base_url, exam_id, AYU)
            for n, typed, accepted in [(1, "JAKARTA", ["Jakarta", "DKI Jakarta"]), (3, "1003", ["1.003", "1003 buah"])]:
                q = a.locator(f"#q{n}")
                expect(q.locator(".typed-text")).to_have_text(typed)
                if (got := texts_of(q.locator(".accepted-answers li"))) != accepted:
                    raise AssertionError(f"question {n} accepted answers {got}")
                expect(q.locator(".auto-mark")).to_have_text("Automatic mark: ✓ Right")
                expect(q.locator(".override-form button")).to_have_text(["Mark right", "Mark wrong"])
            for n in (2, 4):
                expect(a.locator(f"#q{n} .detail-options li")).to_have_count(4)
                expect(a.locator(f"#q{n} .override-form")).to_have_count(0)
            paste_rows = a.locator("#events tr.event").filter(has_text="Tried to paste")
            expect(paste_rows).to_have_count(2)
            expect(paste_rows.locator("td:nth-child(3)")).to_contain_text(["Question 1", "Question 3"])
            override(a, 3, "wrong")
            mark = a.locator("#q3 .override-mark")
            if not OVERRIDE.match(text_of(mark)) or not text_of(mark).startswith("Marked wrong"):
                raise AssertionError(f"override reads {text_of(mark)!r}")
            expect(a.locator("#q3 .auto-mark")).to_have_text("Automatic mark: ✓ Right")
            expect(a.locator("#q3 .override-form button[value=none]")).to_have_text("Remove override")
            if (score := attempt_score(a)) != "3 / 4":
                raise AssertionError(f"Ayu's attempt page shows {score}, want 3 / 4")
            shot(a, "desktop-attempt-ayu.png")
            attempt_page(a, base_url, exam_id, BUDI)
            expect(a.locator("#q1 .auto-mark")).to_have_text("Automatic mark: ✗ Wrong")
            override(a, 1, "right")
            if not text_of(a.locator("#q1 .override-mark")).startswith("Marked right by admin@example.test on "):
                raise AssertionError(f"override reads {text_of(a.locator('#q1 .override-mark'))!r}")
            if (score := attempt_score(a)) != "3 / 4":
                raise AssertionError(f"Budi's attempt page shows {score}, want 3 / 4")
            attempt_page(a, base_url, exam_id, CITRA)
            expect(a.locator(".override-form")).to_have_count(0)

        with step("11 results list, sort by score and the CSV follow the overrides"):
            want = {AYU: ["3 / 4", "2 / 2", "1 / 2"], BUDI: ["3 / 4", "2 / 2", "1 / 2"], CITRA: ["0 / 4", "0 / 2", "0 / 2"]}
            if (got := row_cells(a, base_url, exam_id)) != want:
                raise AssertionError(f"results list {got}, want {want}")
            header = texts_of(a.locator("#attempts thead th"))
            i = header.index("Score")
            if header[i + 1 : i + 3] != [SECTION_A, SECTION_B]:
                raise AssertionError(f"results header {header}")
            shot(a, "desktop-results.png")
            a.locator("#attempts thead a.sort-link", has_text="Score").click()
            expect(a).to_have_url(re.compile(r"sort=score"))
            names = [c.strip() for c in a.locator("#attempts tbody tr[data-attempt-id] td:first-child").all_inner_texts()]
            if names[-1] != CITRA and names[0] != CITRA:
                raise AssertionError(f"sorted by score: {names}")
            body = fetch_csv(a, base_url, exam_id)
            first = body.decode().split("\r\n", 1)[0].split("\n", 1)[0]
            if first != f"name,status,score,total,{SECTION_A},{SECTION_B},started,finished,flags":
                raise AssertionError(f"CSV header {first!r}")
            records = csv_records(body)
            for name, cells in [(AYU, ["3", "4", "2", "1"]), (BUDI, ["3", "4", "2", "1"]), (CITRA, ["0", "4", "0", "0"])]:
                if records.get(name, [])[2:6] != cells:
                    raise AssertionError(f"CSV row of {name} has {records.get(name, [])[2:6]}, want {cells}")
            for typed in ("JAKARTA", "Bandung", "1.003,0"):
                if typed in body.decode():
                    raise AssertionError(f"the CSV has the typed answer {typed!r}")
            (out / "attempts.csv").write_bytes(body)

        with step("12 the students' result pages follow the overrides and do not mention them"):
            ayu.reload()
            expect(ayu.locator("#score")).to_have_text("3 / 4")
            if (marks := review_marks(ayu)) != ["✓ Correct", "✗ Not correct"]:
                raise AssertionError(f"Ayu's review marks {marks}")
            page_text = ayu.locator("body").inner_text()
            if "Marked" in page_text or "overrid" in page_text.lower():
                raise AssertionError("Ayu's result page mentions the override")
            budi.reload()
            expect(budi.locator("#score")).to_have_text("3 / 4")
            if (marks := review_marks(budi)) != ["✓ Correct", "✓ Correct"]:
                raise AssertionError(f"Budi's review marks {marks}")

        with step("13 Remove override gives 4 / 4 everywhere; Mark wrong again gives 3 / 4"):
            attempt_page(a, base_url, exam_id, AYU)
            override(a, 3, "none")
            expect(a.locator("#q3 .override-mark")).to_have_count(0)
            if (score := attempt_score(a)) != "4 / 4":
                raise AssertionError(f"Ayu's attempt page shows {score}, want 4 / 4")
            if (got := row_cells(a, base_url, exam_id)[AYU]) != ["4 / 4", "2 / 2", "2 / 2"]:
                raise AssertionError(f"Ayu's row {got}")
            if csv_records(fetch_csv(a, base_url, exam_id))[AYU][2:6] != ["4", "4", "2", "2"]:
                raise AssertionError("Ayu's CSV row did not follow")
            ayu.reload()
            expect(ayu.locator("#score")).to_have_text("4 / 4")
            attempt_page(a, base_url, exam_id, AYU)
            override(a, 3, "wrong")
            if (score := attempt_score(a)) != "3 / 4":
                raise AssertionError(f"Ayu's attempt page shows {score}, want 3 / 4")
            if fetch_csv(a, base_url, exam_id) != body:
                raise AssertionError("the CSV differs from the one saved in step 11")

        with step("14 no page errors or foreign requests; the one console error is the refused answer"):
            check_logs(logs, base_url, oidc_url, [console_error(f"{base_url}/admin/exams/{exam_id}/questions/new", 422, "Unprocessable Entity")])

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

        def shot(page: Page, name: str) -> None:
            page.screenshot(path=out / name, full_page=True, animations="disabled")

        a = new_context(**DESKTOP).new_page()
        with step("R1 admin signs in; the list has the four exams"):
            teacher_login(a, base_url, admin_email, "Ada Admin")
            ids, codes = {}, {}
            for title, badge in [(TITLE, "Open"), (REIMPORT_TITLE, "Closed"), (COPY_TITLE, "Closed"), (PLAIN_TITLE, "Open")]:
                found = row_by_title(a, title)
                expect(found).to_have_count(1)
                expect(found.locator(".badge")).to_have_text(badge)
                ids[title] = int(found.get_attribute("data-exam-id"))
                codes[title] = exam_code(a, ids[title])
            shot(a, "desktop-exams.png")

        with step("R2 Dewi takes Plain check and is scored; the results list and the CSV have her score"):
            dewi = new_context(**phone).new_page()
            start(dewi, base_url, codes[PLAIN_TITLE], DEWI)
            expect(dewi.locator("section.question")).to_have_count(6)
            first = dewi.locator("section.question").first
            first.locator("label.option").first.click()
            expect(first.locator(".q-state")).to_have_attribute("data-state", "saved")
            score = submit(dewi)
            found = attempt_row(a, base_url, ids[PLAIN_TITLE], DEWI)
            expect(found.locator(".badge")).to_have_text("Submitted")
            expect(found).to_contain_text(score)
            got, total = score.split(" / ")
            if csv_records(fetch_csv(a, base_url, ids[PLAIN_TITLE])).get(DEWI, [])[2:4] != [got, total]:
                raise AssertionError(f"Plain check CSV does not have Dewi's {score}")

        with step("R3 Fill-in check results count only the multiple-choice questions"):
            exam_id = ids[TITLE]
            want = {AYU: ["2 / 4", "1 / 2", "1 / 2"], BUDI: ["1 / 4", "1 / 2", "0 / 2"], CITRA: ["0 / 4", "0 / 2", "0 / 2"]}
            if (got := row_cells(a, base_url, exam_id)) != want:
                raise AssertionError(f"results list {got}, want {want}")
            shot(a, "desktop-results.png")
            records = csv_records(fetch_csv(a, base_url, exam_id))
            for name, cells in [(AYU, ["2", "4", "1", "1"]), (BUDI, ["1", "4", "1", "0"]), (CITRA, ["0", "4", "0", "0"])]:
                if records.get(name, [])[2:6] != cells:
                    raise AssertionError(f"CSV row of {name} has {records.get(name, [])[2:6]}, want {cells}")

        with step("R4 Ayu's attempt page shows questions 1 and 3 with No answer and no options"):
            attempt_page(a, base_url, exam_id, AYU)
            for n in (1, 3):
                expect(a.locator(f"#q{n} .no-answer")).to_have_text("No answer")
                expect(a.locator(f"#q{n} li")).to_have_count(0)
            for n in (2, 4):
                expect(a.locator(f"#q{n} li")).to_have_count(4)
            expect(a.locator("main")).not_to_contain_text("Marked")
            shot(a, "desktop-attempt-ayu.png")

        with step("R5 Citra resumes: questions 1 and 3 have no box and no option"):
            row = attempt_row(a, base_url, exam_id, CITRA)
            resume_code = row.locator("code.resume-code").inner_text().strip()
            citra_context = new_context(**phone)
            citra = citra_context.new_page()
            citra.goto(base_url + "/")
            citra.locator("details.resume summary").click()
            form = citra.locator("#resume-form")
            form.locator("input[name=code]").fill(resume_code)
            form.locator("button[type=submit]").click()
            expect(citra.locator("main#exam")).to_be_visible()
            expect(citra.locator("section.question")).to_have_count(4)
            for n in (1, 3):
                q = citra.locator(f"section.question#q{n}")
                expect(q.locator("input")).to_have_count(0)
                expect(q.locator(".question-body")).to_be_visible()
            for n in (2, 4):
                expect(citra.locator(f"section.question#q{n} input[type=radio]")).to_have_count(4)
            shot(citra, "phone-exam-citra.png")
            citra_context.close()

        with step("R6 Fill-in copy: an editor save is refused, Download has no = lines, Copy answers 500"):
            copy_id = ids[COPY_TITLE]
            a.goto(f"{base_url}/admin/exams/{copy_id}/questions/1")
            expect(a.locator("#question-text")).to_be_visible()
            with a.expect_response(lambda r: r.request.method == "POST" and r.url.endswith(f"/exams/{copy_id}/questions/1")) as info:
                a.locator("#save").click()
            if info.value.status != 422:
                raise AssertionError(f"saving question 1 answered {info.value.status}, want 422")
            expect(a.locator("[role=alert]")).to_have_text(OLD_REFUSED)
            shot(a, "desktop-refused-save.png")
            md = exam_text(download(a, base_url, copy_id, tmp))
            if any(line.lstrip().startswith("=") for line in md.splitlines()):
                raise AssertionError(f"downloaded exam.md has = lines:\n{md}")
            if Q1_TEXT not in md or Q3_TEXT not in md:
                raise AssertionError(f"downloaded exam.md lacks the fill-in questions:\n{md}")
            exam_page(a, base_url, copy_id)
            with a.expect_response(lambda r: r.request.method == "POST" and r.url.endswith(f"/exams/{copy_id}/copy")) as info:
                a.locator("#copy-form button[type=submit]").click()
            if info.value.status != 500:
                raise AssertionError(f"Copy answered {info.value.status}, want 500")
            expect(a.locator("main#error")).to_have_attribute("data-status", "500")
            shot(a, "desktop-copy-500.png")

        with step("R7 no attempt of Fill-in check is gone"):
            if sorted(row_cells(a, base_url, exam_id)) != sorted([AYU, BUDI, CITRA]):
                raise AssertionError("Fill-in check lost an attempt")

        with step("R8 no page errors or foreign requests; console errors are the refused save and the Copy 500"):
            check_logs(
                logs,
                base_url,
                oidc_url,
                [
                    console_error(f"{base_url}/admin/exams/{copy_id}/questions/1", 422, "Unprocessable Entity"),
                    console_error(f"{base_url}/admin/exams/{copy_id}/copy", 500, "Internal Server Error"),
                ],
            )

        browser.close()


def run_returned(base_url: str, admin_email: str, oidc_url: str, out: Path, expect_csv: Path) -> None:
    out.mkdir(parents=True, exist_ok=True)
    logs = []
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        context = browser.new_context(**DESKTOP)
        logs.append(watch(context, base_url))
        a = context.new_page()
        with step("T1 admin signs in"):
            teacher_login(a, base_url, admin_email, "Ada Admin")
            found = row_by_title(a, TITLE)
            expect(found).to_have_count(1)
            exam_id = int(found.get_attribute("data-exam-id"))

        with step("T2 the CSV of Fill-in check is byte-equal to the one the normal run saved"):
            body = fetch_csv(a, base_url, exam_id)
            (out / "attempts.csv").write_bytes(body)
            if body != expect_csv.read_bytes():
                raise AssertionError(f"CSV differs from {expect_csv}:\n{body.decode()}\nwant:\n{expect_csv.read_text()}")

        with step("T3 both overrides are still shown"):
            attempt_page(a, base_url, exam_id, AYU)
            expect(a.locator("#q3 .override-mark")).to_contain_text("Marked wrong by admin@example.test")
            a.screenshot(path=out / "desktop-attempt-ayu.png", full_page=True, animations="disabled")
            attempt_page(a, base_url, exam_id, BUDI)
            expect(a.locator("#q1 .override-mark")).to_contain_text("Marked right by admin@example.test")

        with step("T4 no page errors, foreign requests or console errors"):
            check_logs(logs, base_url, oidc_url, [])

        browser.close()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", required=True)
    add_sign_in_args(parser)
    parser.add_argument("--out", type=Path, default=Path(__file__).parent / "out" / "fillin")
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--rollback", action="store_true", help="check the previous binary on a copy of the data a normal run left")
    mode.add_argument("--returned", action="store_true", help="check this release again on the data the rollback run used")
    parser.add_argument("--expect-csv", type=Path, help="with --returned: the attempts.csv the normal run saved")
    args = parser.parse_args()
    if args.returned != (args.expect_csv is not None):
        parser.error("--returned and --expect-csv go together")
    base_url, oidc_url = args.base_url.rstrip("/"), args.oidc_url.rstrip("/")
    require_cross_site(base_url, oidc_url)
    try:
        if args.rollback:
            run_rollback(base_url, args.admin_email, oidc_url, args.out)
        elif args.returned:
            run_returned(base_url, args.admin_email, oidc_url, args.out, args.expect_csv)
        else:
            run(base_url, args.admin_email, oidc_url, args.out)
    except StepFailed as err:
        print(err, file=sys.stderr)
        return 1
    name = "rollback check" if args.rollback else "returned check" if args.returned else "fill-in check"
    print(f"OK: {name} passed; screenshots in {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
