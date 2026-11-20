# /// script
# requires-python = ">=3.12"
# dependencies = ["playwright==1.63.0"]
# ///
"""Walk-through of Answerbox in real Chromium: every teacher page with scripts
disabled, the student exam and the Open, Copy link and Close controls by
keyboard only, the results list that stops refreshing by itself, a 360 px
phone, the dark and light colour schemes, desktop details (a long owner
email, the results row actions, the save row and the focus, the attempt
counts), and the font, the smooth scroll, the full-window hero and the entry
card of /.
Needs a fresh instance.

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
LONG_TEACHER = "a.very.long.teacher.name@sekolah-contoh.sch.id"
# Names as long as Indonesian names get, to crowd the results table.
LONG_STUDENTS = (
    "Raden Ayu Siti Nurhaliza Kusumawardhani Prameswari Putri Wijayakusuma",
    "Muhammad Rizky Pratama Adiwijaya Kusumanegara Setiabudi Hadiningrat",
)
FONT = "Atkinson Hyperlegible Next"
TOUCH = 44
OPTION = 64
# Computed body colours of the two schemes: (background, text).
SCHEMES = {"dark": ("rgb(17, 26, 21)", "rgb(232, 238, 232)"), "light": ("rgb(244, 245, 238)", "rgb(23, 37, 30)")}


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


def one_line(locator, factor: float, what: str) -> None:
    """The element's text is one line: the height of a range over its contents
    is under factor times its computed line height (a table cell is as high as
    its row, so the element's own box would not tell)."""
    height, line = locator.evaluate(
        """el => {
            const range = document.createRange();
            range.selectNodeContents(el);
            return [range.getBoundingClientRect().height, getComputedStyle(el).lineHeight];
        }"""
    )
    if not line.endswith("px"):
        raise AssertionError(f"{what} has line height {line!r}, want a length")
    line = float(line.removesuffix("px"))
    if height >= factor * line:
        raise AssertionError(f"{what} is {height:.1f} px high, {height / line:.2f} times its line height {line:.1f} px: it wraps")


def status_counts(text: str) -> list[int]:
    """The in progress, submitted and timed out numbers of a counts text."""
    counts = []
    for word in ("in progress", "submitted", "timed out"):
        m = re.search(rf"(\d+) {word}", text)
        if m is None:
            raise AssertionError(f"{text!r} has no number before {word!r}")
        counts.append(int(m.group(1)))
    return counts


def track(page: Page, suffix: str) -> list:
    """The requests of page whose path ends in suffix, recorded from now on."""
    seen = []
    page.on("request", lambda req: urlsplit(req.url).path.endswith(suffix) and seen.append(req))
    return seen


def font_loaded_once(page: Page, fonts: list) -> None:
    """After document.fonts.ready the student font is usable, fetched by exactly one request that got 200."""
    if not page.evaluate(f"() => document.fonts.ready.then(() => document.fonts.check('16px \"{FONT}\"'))"):
        raise AssertionError(f"document.fonts.check says {FONT} is not loaded on {page.url}")
    if len(fonts) != 1:
        raise AssertionError(f"{page.url} made {len(fonts)} .woff2 requests, want 1: {[r.url for r in fonts]}")
    response = fonts[0].response()
    if response is None or response.status != 200:
        raise AssertionError(f"{fonts[0].url} got {response and response.status}, want 200")


def font_family(page: Page, selector: str) -> str:
    return page.locator(selector).first.evaluate("el => getComputedStyle(el).fontFamily")


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


# The resume code refused on purpose; Chromium logs its 422 once per load.
REFUSED_RESUME = "That code is not valid."

# Composites the element's background over its ancestors' (a translucent
# chalk wash shows the board through it) and returns the WCAG contrast of its
# text against that.
CONTRAST_JS = """el => {
    const parse = c => {
        const p = c.match(/rgba?\\(([^)]*)\\)/)[1].split(/[\\s,\\/]+/).filter(Boolean).map(Number);
        return [p[0], p[1], p[2], p.length > 3 ? p[3] : 1];
    };
    const over = (top, bottom) => [0, 1, 2].map(i => top[i] * top[3] + bottom[i] * (1 - top[3])).concat(1);
    const layers = [];
    for (let n = el; n; n = n.parentElement) {
        const c = parse(getComputedStyle(n).backgroundColor);
        if (c[3] > 0) layers.push(c);
        if (c[3] >= 1) break;
    }
    let bg = [255, 255, 255, 1];
    for (let i = layers.length - 1; i >= 0; i--) bg = over(layers[i], bg);
    const fg = over(parse(getComputedStyle(el).color), bg);
    const lum = c => c.slice(0, 3).map(v => {
        v /= 255;
        return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4);
    }).reduce((sum, v, i) => sum + v * [0.2126, 0.7152, 0.0722][i], 0);
    const [hi, lo] = [lum(fg), lum(bg)].sort((a, b) => b - a);
    return (hi + 0.05) / (lo + 0.05);
}"""


def contrast(locator) -> float:
    """The contrast of the element's text once its colour transitions have ended."""
    locator.evaluate("el => Promise.all(el.getAnimations().map(a => a.finished))")
    return locator.evaluate(CONTRAST_JS)


# The code box with id: its value, caret and hint, and its mask measured
# against the input.
MASK_JS = """id => {
    const input = document.getElementById(id), mask = input.closest(".mask-field").querySelector(".mask");
    const typed = mask.querySelector(".typed"), s = getComputedStyle(input), t = getComputedStyle(typed);
    const ib = input.getBoundingClientRect(), tb = typed.getBoundingClientRect();
    const msg = (input.getAttribute("aria-describedby") || "").split(" ").map(i => document.getElementById(i)).find(e => e && e.classList.contains("code-msg"));
    return {
        value: input.value, caret: input.selectionStart, text: mask.textContent, typed: typed.textContent,
        next: mask.querySelectorAll(".next").length, msg: msg ? msg.textContent : null,
        edge: ib.left + parseFloat(s.borderLeftWidth) + parseFloat(s.paddingLeft), left: tb.left,
        middle: ib.top + ib.height / 2, typedMiddle: tb.top + tb.height / 2,
        inputFont: [s.fontFamily, s.fontSize, s.fontWeight, s.letterSpacing],
        typedFont: [t.fontFamily, t.fontSize, t.fontWeight, t.letterSpacing],
    };
}"""


def mask_state(page: Page, id: str) -> dict:
    return page.evaluate(MASK_JS, id)


def entry_words(page: Page) -> dict:
    """The hints the entry card hands to entry.js."""
    card = page.locator(".entry-modes")
    return {k: card.get_attribute(f"data-refuse-{k}") for k in ("letters", "digits", "other")}


def retype(page: Page, selector: str, text: str) -> None:
    """Empty the box, then type text into it key by key."""
    page.locator(selector).fill("")
    page.keyboard.type(text)


def paste(page: Page, selector: str, text: str) -> None:
    """Put text on the real clipboard and press Control+V in the box."""
    page.evaluate("t => navigator.clipboard.writeText(t)", text)
    page.locator(selector).focus()
    page.keyboard.press("Control+V")


def bottom_of(locator) -> float:
    b = box(locator)
    return b["y"] + b["height"]


def closeup(page: Page, locator, path: Path) -> None:
    """A picture of the element with 8 px around it, so the focus ring shows whole."""
    b = box(locator)
    page.screenshot(path=path, clip={"x": b["x"] - 8, "y": b["y"] - 8, "width": b["width"] + 16, "height": b["height"] + 16}, animations="disabled")


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
        with step("1 / shows the start form and the teacher block; Teacher sign-in leads through the provider to the exams list"):
            t.goto(base_url + "/")
            expect(t.locator("#start-form")).to_be_visible()
            expect(t.locator("section.about h2")).to_contain_text("Skip the marking.")
            expect(t.locator("#why")).to_have_count(1)
            expect(t.locator(".features .feature-group")).to_have_count(3)
            expect(t.locator("main#landing img, .features img")).to_have_count(0)
            shot(t, "a-landing-noscript.png")
            t.locator("section.about").get_by_role("link", name="Teacher sign-in").click()
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
        with step("17 360 px /: Start above the fold, two tabs in the window, equal 56 px boxes; /?code= focuses the name box"):
            ps.goto(base_url + "/")
            no_page_overflow(ps)
            start_bottom = bottom_of(ps.locator("#start-form button[type=submit]"))
            if start_bottom > PHONE["viewport"]["height"]:
                raise AssertionError(f"Start ends at {start_bottom:.1f} px, want at most 640")
            tabs = ps.locator(".entry-modes .tab")
            expect(tabs).to_have_count(2)
            # The open tab is 6 px taller and runs into the card: one row shares a bottom.
            if abs(bottom_of(tabs.nth(0)) - bottom_of(tabs.nth(1))) > 1:
                raise AssertionError(f"the tabs end at {bottom_of(tabs.nth(0)):.1f} and {bottom_of(tabs.nth(1)):.1f}, want one row")
            for i in range(2):
                ends_inside(tabs.nth(i), 360, f"tab {i}")
            code_box, name_box = box(ps.locator("#start-code")), box(ps.locator("#name"))
            if abs(code_box["width"] - name_box["width"]) > 0.5 or {round(code_box["height"]), round(name_box["height"])} != {56}:
                raise AssertionError(f"#start-code is {code_box['width']:.1f} × {code_box['height']:.1f}, #name {name_box['width']:.1f} × {name_box['height']:.1f}; want equal widths, 56 px high")
            print(f"  Start ends at {start_bottom:.1f} px; boxes {code_box['width']:.1f} × {code_box['height']:.1f}", flush=True)
            shot(ps, "d-landing-360.png")
            ps.goto(f"{base_url}/?code={phone_code}")
            no_page_overflow(ps)
            expect(ps.locator("input#start-code[type=text]")).to_have_count(0)
            ps.wait_for_function("() => document.activeElement && document.activeElement.id === 'name'")
            if "·" in ps.locator(".landing-code").inner_text():
                raise AssertionError(f".landing-code reads {ps.locator('.landing-code').inner_text()!r}, want no dot")
            for name in ("Join", "Resume"):
                expect(ps.get_by_role("tab", name=name, exact=True)).to_be_visible()
            if (code_start := bottom_of(ps.locator("#start-form button[type=submit]"))) > PHONE["viewport"]["height"]:
                raise AssertionError(f"on /?code= Start ends at {code_start:.1f} px, want at most 640")
            shot(ps, "d-landing-code-360.png")

        with step("18 360 px student exam: timer, save state and Questions in the window, the saved state on one line, 64 px options; the result"):
            ps.keyboard.type("Pia Phone")
            ps.keyboard.press("Enter")
            expect(ps.locator("main#exam")).to_be_visible()
            no_page_overflow(ps)
            options = ps.locator("label.option")
            for i in range(options.count()):
                at_least(options.nth(i), OPTION, f"label.option {i}")
            at_least(ps.locator("#submit-open"), TOUCH, "#submit-open")
            answer_first(ps)
            expect(ps.locator("#nav-toggle")).to_be_visible()
            one_line(ps.locator("#save-state"), 1.5, "the saved #save-state")
            for hook in ("#timer", "#save-state", "#nav-toggle"):
                in_window(ps.locator(hook), PHONE["viewport"], hook)
            shot(ps, "d-student-exam-360.png")
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
            values = pt.locator("dl.settings dd")
            for i in range(values.count()):
                if (w := box(values.nth(i))["width"]) < 120:
                    raise AssertionError(f"settings value {i} ({values.nth(i).inner_text()!r}) is {w:.1f} px wide, want at least 120")
            tiles = pt.locator("#exam-code > span")
            expect(tiles).to_have_count(6)
            tops = [box(tiles.nth(i))["y"] for i in range(6)]
            if max(tops) - min(tops) > 1:
                raise AssertionError(f"the code tiles have tops {tops}, want one line")
            for i in range(6):
                ends_inside(tiles.nth(i), 360, f"code tile {i}")
            if (shown := pt.locator("#exam-code").evaluate("el => el.innerText")) != phone_code:
                raise AssertionError(f"#exam-code innerText is {shown!r}, want {phone_code!r}")
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

        # G. Desktop details at 1280 × 800, as the admin.
        g = new_context(**DESKTOP).new_page()
        with step("21 1280 px exams list: a long owner email stays on one line, no sideways scroll"):
            teacher_login(g, base_url, admin_email, "Desk Admin")
            g.locator("header").get_by_role("link", name="Teachers").click()
            g.locator("#add-teacher-email").fill(LONG_TEACHER)
            g.locator("#add-teacher-form button[type=submit]").click()
            expect(g.locator("#teachers tbody tr").filter(has_text=LONG_TEACHER)).to_have_count(1)
            lt = new_context(**DESKTOP).new_page()
            teacher_login(lt, base_url, LONG_TEACHER, "Long Name")
            long_id = import_exam(lt, fixture("Long owner check"))
            long_code = lt.locator("#exam-code").inner_text().strip()
            open_exam(lt, long_id)
            lt.context.close()
            g.goto(f"{base_url}/admin/exams")
            owner = exam_row(g, long_id).locator("td.owner")
            expect(owner).to_have_text(LONG_TEACHER)
            one_line(owner, 1.6, "the owner cell")
            no_page_overflow(g)
            shot(g, "g-exams-long-owner.png")

        with step("22 1280 px results: View and Delete share a top in every row, also beside long names"):
            for name in LONG_STUDENTS:
                ls = new_context(**DESKTOP).new_page()
                start(ls, base_url, long_code, name)
                answer_first(ls)
                ls.context.close()
            g.goto(f"{base_url}/admin/exams/{long_id}/attempts")
            rows = g.locator("#attempts tbody tr")
            expect(rows).to_have_count(2)
            for i in range(2):
                view = box(rows.nth(i).get_by_role("link", name="View", exact=True))["y"]
                delete = box(rows.nth(i).get_by_role("link", name="Delete", exact=True))["y"]
                if abs(view - delete) > 2:
                    raise AssertionError(f"row {i}: View at y {view:.1f}, Delete at y {delete:.1f}, want the same top")
            results_counts = status_counts(g.locator(".counts").inner_text())
            shot(g, "g-results.png")

        with step("23 1280 × 600 question form: Tab from the question text to Save; the save row never covers the focus"):
            g.set_viewport_size({"width": 1280, "height": 600})
            g.goto(f"{base_url}/admin/exams/{written_id}/questions/2")
            expect(g.locator("#question-form textarea[name=option]")).to_have_count(5)
            g.locator("#question-text").focus()
            for presses in range(1, 101):
                g.keyboard.press("Tab")
                saved, inside, bottom, bar, what = g.evaluate(
                    """() => {
                        const el = document.activeElement, row = document.querySelector(".form-actions");
                        return [el.matches("#save"), row.contains(el), el.getBoundingClientRect().bottom,
                                row.getBoundingClientRect().top, el.outerHTML.slice(0, 80)];
                    }"""
                )
                if not inside and bottom > bar:
                    raise AssertionError(f"Tab {presses}: {what} ends at y {bottom:.1f}, below the save row's top {bar:.1f}")
                if saved:
                    break
            else:
                raise AssertionError("100 presses of Tab never reached #save")
            g.set_viewport_size(DESKTOP["viewport"])

        with step("24 1280 px exam page: the results link is named Results; the counts equal the results page's"):
            g.goto(f"{base_url}/admin/exams/{long_id}")
            expect(g.locator("nav.exam-links a[href$='/attempts']")).to_have_accessible_name("Results")
            if (page_counts := status_counts(g.locator("#attempt-counts").inner_text())) != results_counts:
                raise AssertionError(f"#attempt-counts shows {page_counts}, the results page {results_counts}")
            shot(g, "g-exam.png")

        # H. The student font, the smooth scroll and the full-window hero.
        with step("25 font: / and the student exam page fetch Atkinson Hyperlegible Next once; question text uses it, the exams list title does not"):
            fp = new_context(**DESKTOP).new_page()
            fonts = track(fp, ".woff2")
            fp.goto(base_url + "/")
            font_loaded_once(fp, fonts)
            student = new_context(**DESKTOP).new_page()
            start(student, base_url, phone_code, "Fina Font")
            fe = new_context(storage_state=student.context.storage_state(), **DESKTOP).new_page()
            fonts = track(fe, ".woff2")
            fe.goto(base_url + "/exam")
            expect(fe.locator("main#exam")).to_be_visible()
            font_loaded_once(fe, fonts)
            for where, page in (("the student exam page", fe), ("the teacher's exam page", g)):
                if not (family := font_family(page, ".question-body")).startswith(f'"{FONT}"'):
                    raise AssertionError(f".question-body on {where} has font-family {family}, want {FONT} first")
            g.goto(f"{base_url}/admin/exams")
            if (family := font_family(g, "h1")).startswith(f'"{FONT}"'):
                raise AssertionError(f"the exams list h1 has font-family {family}, want the UI font")

        with step("26 motion: smooth scroll only without reduced motion; Learn more puts #why at the top"):
            for motion, want in (("no-preference", "smooth"), ("reduce", "auto")):
                m = new_context(reduced_motion=motion, **DESKTOP).new_page()
                m.goto(base_url + "/")
                if (got := m.evaluate("() => getComputedStyle(document.documentElement).scrollBehavior")) != want:
                    raise AssertionError(f"reduced motion {motion}: html scroll-behavior is {got!r}, want {want!r}")
                m.locator("section.about").get_by_role("link", name="Learn more").click()
                m.wait_for_function("() => Math.abs(document.getElementById('why').getBoundingClientRect().top) <= 1", timeout=5_000)

        with step("27 hero: on a desktop the first view is the window's height with either tab, the entry does not move; a phone keeps its natural height"):
            for viewport in ({"width": 1280, "height": 800}, {"width": 1440, "height": 900}):
                hp = new_context(viewport=viewport).new_page()
                hp.goto(base_url + "/")
                tops = []
                for tab in ("Join", "Resume"):
                    hp.get_by_role("tab", name=tab, exact=True).click()
                    expect(hp.get_by_role("tab", name=tab, exact=True)).to_have_attribute("aria-selected", "true")
                    height = box(hp.locator(".landing-split"))["height"]
                    if abs(height - viewport["height"]) > 1:
                        raise AssertionError(f"{viewport['width']} × {viewport['height']}, {tab}: .landing-split is {height:.1f} px high, want the window's {viewport['height']}")
                    tops.append(box(hp.locator(".entry-modes"))["y"])
                    print(f"  {viewport['width']} × {viewport['height']} {tab}: hero {height:.1f} px, entry at y {tops[-1]:.1f}", flush=True)
                if abs(tops[0] - tops[1]) > 1:
                    raise AssertionError(f"{viewport['width']} px: .entry-modes starts at y {tops[0]:.1f} with Join, {tops[1]:.1f} with Resume")
                shot(hp, f"h-hero-{viewport['width']}-resume.png")
                hp.context.close()
            hp = new_context(**PHONE).new_page()
            hp.goto(base_url + "/")
            if (minimum := hp.locator(".landing-split").evaluate("el => getComputedStyle(el).minHeight")) not in ("0px", "auto"):
                raise AssertionError(f"360 px: .landing-split has min-height {minimum}, want none")
            hp.context.close()

        # I. The entry card of /: the tabs, the two code boxes, without scripts too.
        with step("28 tabs by keyboard: Shift+Tab from the code box to Join, arrows, Home and End; closed tabs readable in both schemes"):
            et = new_context(**DESKTOP).new_page()
            et.goto(base_url + "/")
            tablist = et.locator(".entry-modes .tablist")
            expect(tablist).to_have_attribute("role", "tablist")
            expect(tablist).to_have_accessible_name(tablist.get_attribute("data-label"))
            expect(et.get_by_role("tab")).to_have_count(2)
            join, resume = et.get_by_role("tab", name="Join", exact=True), et.get_by_role("tab", name="Resume", exact=True)
            expect(et.locator(".entry-modes input[type=radio]")).to_have_count(0)
            et.wait_for_function("() => document.activeElement && document.activeElement.id === 'start-code'")
            et.keyboard.press("Shift+Tab")
            et.wait_for_function("() => document.activeElement && document.activeElement.id === 'tab-join'")
            expect(join).to_have_attribute("aria-selected", "true")
            expect(join).to_have_attribute("tabindex", "0")
            expect(resume).to_have_attribute("tabindex", "-1")
            ring(et)

            def opened(tab, shown: str, gone: str) -> None:
                expect(tab).to_have_attribute("aria-selected", "true")
                expect(tab).to_be_focused()
                expect(tab).to_have_attribute("tabindex", "0")
                for part in ("panel", "lead"):
                    expect(et.locator(f"#{part}-{shown}")).to_be_visible()
                    expect(et.locator(f"#{part}-{gone}")).to_be_hidden()

            for key, tab, shown, gone in (
                ("ArrowRight", resume, "resume", "join"),
                ("ArrowRight", join, "join", "resume"),
                ("End", resume, "resume", "join"),
                ("Home", join, "join", "resume"),
                ("ArrowLeft", resume, "resume", "join"),
            ):
                et.keyboard.press(key)
                opened(tab, shown, gone)
            shot(et, "i-tabs-keyboard.png")
            et.keyboard.press("Tab")
            expect(et.locator("#code")).to_be_focused()
            et.context.close()
            for scheme in ("light", "dark"):
                cp = new_context(color_scheme=scheme, **DESKTOP).new_page()
                cp.goto(base_url + "/")
                closed = cp.get_by_role("tab", name="Resume", exact=True)
                expect(closed).to_have_attribute("aria-selected", "false")
                cp.mouse.move(0, 0)
                rest = contrast(closed)
                closed.hover()
                hovered = contrast(closed)
                print(f"  {scheme}: closed tab {rest:.2f} : 1, hovered {hovered:.2f} : 1", flush=True)
                if rest < 4.5 or hovered < 4.5:
                    raise AssertionError(f"{scheme}: the closed tab's text has {rest:.2f} : 1, hovered {hovered:.2f} : 1, want at least 4.5")
                cp.context.close()

        ic = new_context(permissions=["clipboard-read", "clipboard-write"], device_scale_factor=3, **PHONE).new_page()
        with step("29 exam code box: the mask lines up with the text, letters go upper-case, refused characters get a hint"):
            ic.goto(base_url + "/")
            words = entry_words(ic)
            code_input = ic.locator("#start-code")
            if code_input.get_attribute("placeholder") is not None:
                raise AssertionError("#start-code keeps its placeholder with the script")
            expect(ic.locator("#start-code").locator("xpath=..")).to_have_class(re.compile(r"\bmask-field\b.*\bis-live\b"))
            for value in ("", "F", "FG2", "FG2J62"):
                retype(ic, "#start-code", value)
                m = mask_state(ic, "start-code")
                want = value + "_" * (6 - len(value))
                if m["value"] != value or m["text"] != want:
                    raise AssertionError(f"typed {value!r}: value {m['value']!r}, mask {m['text']!r}, want {want!r}")
                if abs(m["left"] - m["edge"]) > 0.5 or abs(m["typedMiddle"] - m["middle"]) > 1:
                    raise AssertionError(f"typed {value!r}: the mask's text starts at x {m['left']:.2f}, middle {m['typedMiddle']:.2f}; the input's at {m['edge']:.2f}, {m['middle']:.2f}")
                if m["typedFont"] != m["inputFont"]:
                    raise AssertionError(f"typed {value!r}: mask font {m['typedFont']}, input font {m['inputFont']}")
                if m["next"] != (1 if len(value) < 6 else 0):
                    raise AssertionError(f"typed {value!r}: {m['next']} .next slots while focused")
                code_input.blur()
                if (n := mask_state(ic, "start-code")["next"]) != 0:
                    raise AssertionError(f"typed {value!r}: {n} .next slots after blur, want none")
                code_input.focus()
                code_input.press("End")
                name = {"": "empty", "FG2": "fg2", "FG2J62": "full"}.get(value)
                if name:
                    closeup(ic, ic.locator("#start-form .field").first, out / f"i-code-{name}.png")
            msg = ic.locator("#code-msg")
            retype(ic, "#start-code", "fg2")
            expect(code_input).to_have_value("FG2")
            for key, hint in (("o", words["letters"]), ("0", words["digits"]), ("!", words["other"])):
                ic.keyboard.type(key)
                expect(code_input).to_have_value("FG2")
                expect(msg).to_have_text(hint)
            ic.keyboard.type("j")
            expect(code_input).to_have_value("FG2J")
            expect(msg).to_have_text("")
            for key in ("-", " "):
                ic.keyboard.type(key)
                expect(code_input).to_have_value("FG2J")
                expect(msg).to_have_text("")
            ic.keyboard.type("62x9")
            expect(code_input).to_have_value("FG2J62")

        with step("30 paste from the clipboard: a code in any shape, a link with ?code=, a link without one"):
            for text in ("fg2j62", "FG2-J62", " f g 2 j 6 2 ", "https://exam.school.id/?code=fg2j62"):
                ic.locator("#start-code").fill("")
                paste(ic, "#start-code", text)
                expect(ic.locator("#start-code")).to_have_value("FG2J62")
            retype(ic, "#start-code", "fg2")
            paste(ic, "#start-code", "J62")
            expect(ic.locator("#start-code")).to_have_value("FG2J62")
            for link in ("https://exam.school.id/", "https://exam.school.id/?code=%"):
                retype(ic, "#start-code", "fg2")
                paste(ic, "#start-code", link)
                expect(ic.locator("#start-code")).to_have_value("FG2")
                expect(ic.locator("#code-msg")).to_have_text(ic.locator("#start-code").get_attribute("data-refuse-link"))
            ic.get_by_role("tab", name="Resume", exact=True).click()
            for text in ("abcd efgh", "ABCD-EFGH"):
                ic.locator("#code").fill("")
                paste(ic, "#code", text)
                expect(ic.locator("#code")).to_have_value("ABCD-EFGH")
            paste(ic, "#code", "https://exam.school.id/?code=abcdefgh")
            expect(ic.locator("#code")).to_have_value("ABCD-EFGH")
            expect(ic.locator("#resume-msg")).to_have_text(ic.locator("#code").get_attribute("data-refuse-link"))

        with step("31 resume box: the dash comes and goes by itself; a refused code comes back focused, invalid and described"):
            resume_input = ic.locator("#code")
            retype(ic, "#code", "abcd")
            expect(resume_input).to_have_value("ABCD")
            if (text := mask_state(ic, "code")["text"]) != "ABCD-____":
                raise AssertionError(f"the resume mask reads {text!r}, want 'ABCD-____'")
            closeup(ic, ic.locator("#resume-form .mask-field"), out / "i-resume-abcd.png")
            ic.keyboard.type("e")
            expect(resume_input).to_have_value("ABCD-E")
            ic.keyboard.press("Backspace")
            expect(resume_input).to_have_value("ABCD")
            ic.keyboard.type("efgh")
            expect(resume_input).to_have_value("ABCD-EFGH")
            resume_input.evaluate("el => el.setSelectionRange(5, 5)")
            ic.keyboard.press("Backspace")
            expect(resume_input).to_have_value("ABCE-FGH")
            retype(ic, "#code", "zzzzzzzz")
            expect(resume_input).to_have_value("ZZZZ-ZZZZ")
            ic.locator("#resume-form button[type=submit]").click()
            refused_resumes = 1
            expect(ic.get_by_role("tab", name="Resume", exact=True)).to_have_attribute("aria-selected", "true")
            expect(ic.locator("#panel-resume [role=alert]")).to_have_text(REFUSED_RESUME)
            expect(resume_input).to_be_focused()
            expect(resume_input).to_have_value("ZZZZ-ZZZZ")
            if (caret := resume_input.evaluate("el => el.selectionStart")) != 9:
                raise AssertionError(f"the refused code's caret is at {caret}, want 9")
            expect(resume_input).to_have_attribute("aria-invalid", "true")
            expect(resume_input).to_have_accessible_description(re.compile(re.escape(REFUSED_RESUME)))
            shot(ic, "i-resume-refused-360.png")

        with step("32 composition (a keyboard that sends a word in progress): the value is the keyboard's until it commits"):
            ic.goto(base_url + "/")
            ic.evaluate(
                """() => { window.__composition = {start: 0, end: 0};
                    document.addEventListener("compositionstart", () => window.__composition.start++, true);
                    document.addEventListener("compositionend", () => window.__composition.end++, true); }"""
            )
            cdp = ic.context.new_cdp_session(ic)
            words = entry_words(ic)

            def compose(text: str) -> None:
                cdp.send("Input.imeSetComposition", {"text": text, "selectionStart": len(text), "selectionEnd": len(text)})

            ic.locator("#start-code").focus()
            for prefix in ("f", "fg", "fg2", "fg2o"):
                compose(prefix)
                m = mask_state(ic, "start-code")
                if m["value"] != prefix or m["typed"] != prefix:
                    raise AssertionError(f"composing {prefix!r}: value {m['value']!r}, mask text {m['typed']!r}")
            expect(ic.locator("#code-msg")).to_have_text(words["letters"])
            cdp.send("Input.insertText", {"text": "fg2oj62"})
            m = mask_state(ic, "start-code")
            if (m["value"], m["caret"], m["msg"]) != ("FG2J62", 6, words["letters"]):
                raise AssertionError(f"committed 'fg2oj62': value {m['value']!r}, caret {m['caret']}, hint {m['msg']!r}")
            ic.get_by_role("tab", name="Resume", exact=True).click()
            ic.locator("#code").focus()
            for prefix in ("a", "ab", "abc", "abcd", "abcde"):
                compose(prefix)
            m = mask_state(ic, "code")
            if (m["value"], m["text"]) != ("abcde", "abcde___"):
                raise AssertionError(f"composing 'abcde': value {m['value']!r}, mask {m['text']!r}")
            cdp.send("Input.insertText", {"text": "abcdefgh"})
            m = mask_state(ic, "code")
            if (m["value"], m["caret"]) != ("ABCD-EFGH", 9):
                raise AssertionError(f"committed 'abcdefgh': value {m['value']!r}, caret {m['caret']}")
            ic.locator("#code").fill("")
            compose("ab")
            compose("")
            m = mask_state(ic, "code")
            if (m["value"], m["text"]) != ("", "____-____"):
                raise AssertionError(f"cancelled composition: value {m['value']!r}, mask {m['text']!r}")
            counts = ic.evaluate("() => window.__composition")
            if counts["start"] < 3 or counts["end"] < 3:
                raise AssertionError(f"compositions seen {counts}, want at least 3 started and ended")
            cdp.detach()
            ic.context.close()

        with step("33 without scripts: the tabs are radios, the boxes plain inputs that hold a code with separators"):
            nj = new_context(java_script_enabled=False, **PHONE).new_page()
            nj.goto(base_url + "/")
            expect(nj.locator("[role=tab]")).to_have_count(0)
            radios = nj.locator(".entry-modes input[type=radio]")
            expect(radios).to_have_count(2)
            expect(nj.locator("#tab-join-r")).to_be_checked()

            def showing(shown: str, gone: str) -> None:
                for part in ("panel", "lead"):
                    expect(nj.locator(f"#{part}-{shown}")).to_be_visible()
                    expect(nj.locator(f"#{part}-{gone}")).to_be_hidden()

            showing("join", "resume")
            expect(nj.locator("#start-code")).to_have_attribute("placeholder", "______")
            expect(nj.locator("#start-form .mask")).to_be_hidden()
            if (start_bottom := bottom_of(nj.locator("#start-form button[type=submit]"))) > PHONE["viewport"]["height"]:
                raise AssertionError(f"without scripts Start ends at {start_bottom:.1f} px, want at most 640")
            shot(nj, "i-noscript-360.png")
            nj.locator(".entry-modes .tab-resume").click()
            expect(nj.locator("#tab-resume-r")).to_be_checked()
            showing("resume", "join")
            expect(nj.locator("#code")).to_have_attribute("placeholder", "____-____")
            nj.keyboard.press("ArrowLeft")
            expect(nj.locator("#tab-join-r")).to_be_checked()
            showing("join", "resume")
            style, width = nj.locator(".entry-modes .tab-join").evaluate("el => [getComputedStyle(el).outlineStyle, parseFloat(getComputedStyle(el).outlineWidth)]")
            if style == "none" or width < 2:
                raise AssertionError(f"the focused Join radio's label has outline {style} {width}px")
            typed = f" {phone_code[:3].lower()}-{phone_code[3:].lower()} "
            nj.locator("#start-code").click()
            nj.keyboard.type(typed)
            expect(nj.locator("#start-code")).to_have_value(typed)
            nj.locator("#name").fill("Nia Noscript")
            nj.keyboard.press("Enter")
            expect(nj.locator("main#exam")).to_be_visible()
            nj.context.close()
            nr = new_context(java_script_enabled=False, **PHONE).new_page()
            nr.goto(base_url + "/")
            nr.locator(".entry-modes .tab-resume").click()
            nr.locator("#code").click()
            nr.keyboard.type("zzzz zzzz")
            nr.keyboard.press("Enter")
            refused_resumes += 1
            expect(nr.locator("#tab-resume-r")).to_be_checked()
            expect(nr.locator("#panel-resume [role=alert]")).to_have_text(REFUSED_RESUME)
            expect(nr.locator("#code")).to_be_focused()
            expect(nr.locator("#code")).to_have_value("zzzz zzzz")
            shot(nr, "i-noscript-resume-refused-360.png")
            nr.goto(f"{base_url}/?code={phone_code}")
            expect(nr.locator("h1")).to_have_text("Phone check")
            for label in (".tab-join", ".tab-resume"):
                expect(nr.locator(f".entry-modes {label}")).to_be_visible()
            expect(nr.locator("#name")).to_be_focused()
            nr.context.close()

        with step("34 no console errors but the refused resume codes', no page errors or foreign requests"):
            refused = f"{base_url}/resume: Failed to load resource: the server responded with a status of 422 (Unprocessable Entity)"
            problems, seen = [], 0
            for log in logs:
                for e in log["console_errors"]:
                    if e == refused:
                        seen += 1
                    else:
                        problems.append(f"console error: {e}")
                problems += [f"page error: {e}" for e in log["page_errors"]]
                problems += [f"foreign request: {u}" for u in foreign_requests(log["requests"], base_url, oidc_url)]
            if seen != refused_resumes:
                problems.append(f"{seen} times {refused!r} for {refused_resumes} refused resume codes, want one each")
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
