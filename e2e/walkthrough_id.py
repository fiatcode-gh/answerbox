# /// script
# requires-python = ">=3.12"
# dependencies = ["playwright==1.63.0"]
# ///
"""Walk-through of Answerbox in Indonesian in real Chromium, four passes: a
360 × 640 phone and a 1280 × 800 desktop, each in the light and the dark
colour scheme. A student whose browser asks for Indonesian (id-ID) starts,
answers, is flagged by the monitor, submits and reads the review; two more
students stay in progress and resume; a teacher whose browser asks for English
presses Indonesia once on the sign-in page and walks results, the attempt
page, History, a copy and its editor, archive, Teachers, two 404 pages and Log
out. On every page: html[lang] is id, nothing overflows or is clipped, the
language switch overlaps nothing, and no English word is left. The phone
passes keep Start above the fold and the exam bar on one row; the desktop
passes keep the hero the window's height. Screenshots per pass, with an
index.md. Needs a fresh instance.

uv run e2e/walkthrough_id.py --base-url URL --admin-email EMAIL --oidc-url URL --out DIR
"""

import argparse
import csv
import re
import sys
import tempfile
import zipfile
from contextlib import contextmanager
from pathlib import Path

from playwright.sync_api import Page, expect, sync_playwright

from common import (
    add_sign_in_args,
    foreign_requests,
    import_exam,
    open_exam,
    require_cross_site,
    show_resume,
    teacher_login,
    watch,
    write_png,
)

DESKTOP = {"width": 1280, "height": 800}
PHONE = {"width": 360, "height": 640}
PASSES = [
    ("phone-light", PHONE, "light"),
    ("phone-dark", PHONE, "dark"),
    ("desktop-light", DESKTOP, "light"),
    ("desktop-dark", DESKTOP, "dark"),
]
TOUCH = 44
OPTION = 64
BOX = 56

OPEN_WORD = "Dibuka"
CSV_HEAD = ["nama", "status", "nilai", "total"]
CSV_TAIL = ["mulai", "selesai", "catatan"]
CSV_SUBMITTED, CSV_IN_PROGRESS = "dikumpulkan", "mengerjakan"

SECTION_A, SECTION_B = "Bagian A: Bilangan", "Bagian B: Bangun datar"
SECTION_A_TEXT, SECTION_B_TEXT = "Pilih satu jawaban yang paling tepat.", "Baca setiap soal dengan teliti."
# (question text, options with "*" on the right one) or (text, "= accepted answer").
QUESTIONS = [
    ("Berapakah hasil 12 + 30?", ["32", "42 *", "52"]),
    ("Bilangan manakah yang genap?", ["7", "9", "14 *"]),
    ("Berapa jumlah sisi segi empat pada gambar?", ["3", "4 *", "5"]),
    ("Berapakah 1.000 + 3? Tulis dengan angka.", "= 1.003"),
]
PICKS = {1: "42", 2: "14", 3: "4"}
TYPED = "1003"
STUDENT, SECOND, THIRD = "Sari Wulandari", "Budi Santoso", "Citra Dewi"
REFUSED_CODE = "ZZZZZZ"
WRONG_RESUME = "ZZZZ-ZZZZ"
REFUSED_EMAIL = "guru@sekolah"  # passes the browser's email check, the server wants a dot in the domain
# Whole English words that must not be left on an Indonesian page.
SENTINELS = re.compile(
    r"\b(Exam|Question|Answer|Save|Saved|Start|Submit|Open|Close|Results|Settings|Remove|Cancel|Teacher|Student|Score)\b"
)
# Refused page loads the walk-through makes on purpose: (path pattern, status, why).
EXEMPT = [
    (r"/start", 422, "a start with an unknown code"),
    (r"/resume", 422, "a wrong resume code"),
    (r"/admin/exams/\d+/attempts/\d+/rename", 422, "a rename to an empty name"),
    (r"/admin/exams/\d+/questions/\d+", 422, "a question saved without text"),
    (r"/admin/teachers", 422, "a teacher email without a dot in the domain"),
    (r"/admin/tidak-ada", 404, "the 404 page under /admin"),
    (r"/tidak-ada", 404, "the 404 page outside /admin"),
]
# The answer saves aborted on purpose to show the retrying state.
ABORTED = r"/exam: Failed to load resource: net::ERR_FAILED"


class StepFailed(Exception):
    pass


@contextmanager
def step(name: str):
    print(f"step: {name}", flush=True)
    try:
        yield
    except Exception as err:  # every failure names its step
        raise StepFailed(f"FAIL [{name}]: {err}") from err


def exam_md(title: str) -> str:
    lines = [f"# {title}", "shuffle: none", "show_result: review", "monitor: on", "duration: 30", ""]
    lines += [f"## {SECTION_A}", SECTION_A_TEXT, ""]
    for n, (text, body) in enumerate(QUESTIONS, start=1):
        if n == 3:
            lines += [f"## {SECTION_B}", SECTION_B_TEXT, ""]
        lines.append(f"{n}. {text}")
        if n == 3:
            lines.append("![segi empat](bangun.png)")
        lines += [body] if isinstance(body, str) else [f"- {o}" for o in body]
        lines.append("")
    return "\n".join(lines)


def write_exam_zip(dir: Path, title: str) -> Path:
    dir.mkdir(parents=True, exist_ok=True)
    image = write_png(dir / "bangun.png", 160, 120)
    path = dir / "exam.zip"
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as z:
        z.writestr("exam.md", exam_md(title))
        z.write(image, "bangun.png")
    return path


def box(locator):
    b = locator.bounding_box()
    if b is None:
        raise AssertionError(f"{locator} is not rendered")
    return b


def bottom_of(locator) -> float:
    b = box(locator)
    return b["y"] + b["height"]


def ends_inside(locator, width: float, what: str) -> None:
    b = box(locator)
    if b["x"] < 0 or b["x"] + b["width"] > width:
        raise AssertionError(f"{what} spans x {b['x']:.1f} to {b['x'] + b['width']:.1f}, want inside 0 to {width}")


def in_window(locator, viewport: dict, what: str) -> None:
    b = box(locator)
    if b["x"] < 0 or b["y"] < 0 or b["x"] + b["width"] > viewport["width"] or b["y"] + b["height"] > viewport["height"]:
        raise AssertionError(f"{what} at {b}, want inside the {viewport['width']} × {viewport['height']} window")


def at_least(locator, height: float, what: str) -> None:
    if (h := box(locator)["height"]) < height:
        raise AssertionError(f"{what} is {h:.1f} px high, want at least {height}")


def one_row(boxes: dict) -> None:
    """The boxes share a horizontal band: every top is above every bottom."""
    tops = {k: b["y"] for k, b in boxes.items()}
    bottoms = {k: b["y"] + b["height"] for k, b in boxes.items()}
    if max(tops.values()) >= min(bottoms.values()):
        raise AssertionError(f"not one row: tops {tops}, bottoms {bottoms}")


def one_line(locator, factor: float, what: str) -> None:
    """The element's text is one line: a range over its contents is under factor line heights."""
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


# Visible elements whose text is cut or that stick out of the window sideways.
CLIPPED_JS = """() => {
    const vw = document.documentElement.clientWidth;
    const bad = [];
    for (const el of document.querySelectorAll("button, a.button, .badge, .tab, label, summary, th, h1, h2, .eyebrow, #save-state, #timer")) {
        if (el.closest(".overflow-auto, .signed-in-as")) continue;
        if (!el.checkVisibility({visibilityProperty: true})) continue;
        const r = el.getBoundingClientRect();
        if (r.width <= 1 || r.height <= 1) continue;
        const cut = el.scrollWidth > el.clientWidth + 1;
        const out = r.left < -0.5 || r.right > vw + 0.5;
        if (cut || out) bad.push(`${el.outerHTML.slice(0, 90)}: x ${r.left.toFixed(1)} to ${r.right.toFixed(1)} of ${vw}, scrollWidth ${el.scrollWidth} > clientWidth ${el.clientWidth}`);
    }
    return bad;
}"""

# Elements with their own text, and controls, images and links, whose box
# meets the closed switch's button.
OVERLAP_JS = """() => {
    const sw = document.getElementById("lang-switch");
    const s = sw.querySelector("summary").getBoundingClientRect();
    const hits = [];
    for (const el of document.body.querySelectorAll("*")) {
        if (sw.contains(el) || el.contains(sw)) continue;
        if (!el.checkVisibility({visibilityProperty: true})) continue;
        const own = [...el.childNodes].some(n => n.nodeType === 3 && n.textContent.trim());
        if (!own && !el.matches("img, svg, input, button, select, textarea, a, summary, video, canvas")) continue;
        const r = el.getBoundingClientRect();
        if (r.width <= 1 || r.height <= 1) continue;
        const w = Math.min(r.right, s.right) - Math.max(r.left, s.left);
        const h = Math.min(r.bottom, s.bottom) - Math.max(r.top, s.top);
        if (w > 0.5 && h > 0.5) hits.push(`${el.outerHTML.slice(0, 90)} at ${r.left.toFixed(1)},${r.top.toFixed(1)} ${r.width.toFixed(1)}×${r.height.toFixed(1)}`);
    }
    return [hits, [s.left, s.top, s.width, s.height]];
}"""


def check_switch(page: Page, viewport: dict) -> None:
    """The switch is visible, overlaps nothing, and its button and the two
    buttons of its menu (opened through the summary, then closed) are 44 × 44."""
    switch = page.locator("#lang-switch")
    summary = switch.locator("summary")
    summary.scroll_into_view_if_needed()
    expect(summary).to_be_visible()
    in_window(summary, viewport, "the switch's button")
    hits, at = page.evaluate(OVERLAP_JS)
    if hits:
        raise AssertionError(f"the switch at {at} overlaps: " + "; ".join(hits))
    sizes = [("summary", box(summary))]
    summary.click()
    buttons = switch.locator("form button[name=lang]")
    expect(buttons).to_have_count(2)
    for i in range(2):
        expect(buttons.nth(i)).to_be_visible()
        sizes.append((buttons.nth(i).inner_text(), box(buttons.nth(i))))
        ends_inside(buttons.nth(i), viewport["width"], f"the switch's {buttons.nth(i).inner_text()} button")
    summary.click()
    expect(buttons.first).to_be_hidden()
    small = [f"{what} {b['width']:.1f} × {b['height']:.1f}" for what, b in sizes if b["width"] < TOUCH or b["height"] < TOUCH]
    if small:
        raise AssertionError(f"switch targets under {TOUCH} × {TOUCH}: {small}")
    page.evaluate("window.scrollTo(0, 0)")


def no_english(page: Page, own: list[str]) -> None:
    text = page.evaluate("() => document.body.innerText")
    for s in sorted(own, key=len, reverse=True):
        text = text.replace(s, " ")
    found = sorted({m.group(0) for m in SENTINELS.finditer(text)})
    if found:
        lines = [line.strip() for line in text.splitlines() if SENTINELS.search(line)]
        raise AssertionError(f"English words {found} on {page.url}: {lines}")


def no_page_overflow(page: Page) -> None:
    widths = page.evaluate("() => [document.documentElement.scrollWidth, document.documentElement.clientWidth]")
    if widths[0] > widths[1]:
        raise AssertionError(f"page overflows horizontally on {page.url}: scrollWidth {widths[0]} > clientWidth {widths[1]}")


def pick(page: Page, n: int, option: str) -> None:
    labels = page.locator(f"section.question#q{n} label.option")
    for j in range(labels.count()):
        if labels.nth(j).locator(".option-body").inner_text().strip() == option:
            labels.nth(j).click()
            return
    raise AssertionError(f"question {n} has no option {option!r}")


def picked(page: Page, n: int) -> str | None:
    label = page.locator(f"section.question#q{n} label.option").filter(has=page.locator("input[type=radio]:checked"))
    return label.locator(".option-body").inner_text().strip() if label.count() else None


def wait_saved(page: Page, timeout: float = 10_000) -> None:
    expect(page.locator("#save-state")).to_have_attribute("data-state", "saved", timeout=timeout)


def question_tops(page: Page) -> list[float]:
    return page.evaluate("() => [...document.querySelectorAll('section.question')].map(q => q.getBoundingClientRect().top + scrollY)")


def copy_question(page: Page) -> None:
    """Select question 1's text and press Ctrl+C (dispatching the event if Chromium fires none)."""
    body = page.locator("section.question#q1 .question-body")
    body.click(click_count=3)
    page.evaluate("window.__copied = false; document.addEventListener('copy', () => { window.__copied = true; }, {once: true})")
    page.keyboard.press("Control+C")
    if not page.evaluate("window.__copied"):
        body.dispatch_event("copy")


def read_csv(path: Path) -> list[list[str]]:
    with open(path, newline="", encoding="utf-8-sig") as f:
        return [r for r in csv.reader(f) if r]


def run(base_url: str, admin_email: str, oidc_url: str, out: Path) -> dict:
    out.mkdir(parents=True, exist_ok=True)
    logs = []
    index = {}
    measured = {}
    with sync_playwright() as p, tempfile.TemporaryDirectory() as tmp:
        browser = p.chromium.launch(headless=True)

        def new_context(**kwargs):
            context = browser.new_context(**kwargs)
            logs.append(watch(context, base_url))
            return context

        for pass_name, viewport, scheme in PASSES:
            phone = viewport is PHONE
            dir = out / pass_name
            dir.mkdir(parents=True, exist_ok=True)
            shots = index[pass_name] = []
            title = f"Latihan Matematika {pass_name.replace('-', ' ')}"
            own = [title, SECTION_A, SECTION_B, SECTION_A_TEXT, SECTION_B_TEXT, STUDENT, SECOND, THIRD, admin_email, "Indonesia", "English"]
            own += [q for q, _ in QUESTIONS]

            def shot(page: Page, name: str, what: str, full: bool = True) -> None:
                file = f"{len(shots) + 1:02d}-{name}.png"
                page.screenshot(path=dir / file, full_page=full, animations="disabled")
                shots.append((file, what))

            def check_page(page: Page, name: str, what: str, switch: bool = True) -> None:
                """The checks every page gets, then its full-page screenshot."""
                expect(page.locator("html")).to_have_attribute("lang", "id")
                no_page_overflow(page)
                if clipped := page.evaluate(CLIPPED_JS):
                    raise AssertionError(f"clipped on {page.url}:\n    " + "\n    ".join(clipped))
                if switch:
                    check_switch(page, viewport)
                no_english(page, own)
                shot(page, name, what)

            context_args = {"viewport": viewport, "color_scheme": scheme}

            # 1. Teacher: sign-in, exams list, import, exam page, open, projector.
            t = new_context(locale="en-US", **context_args).new_page()
            with step(f"{pass_name} 1 teacher (en-US) presses Indonesia on the sign-in page, signs in, imports and opens the exam"):
                t.goto(base_url + "/admin/login")
                expect(t.locator("html")).to_have_attribute("lang", "en")
                t.locator("#lang-switch summary").click()  # the switch is a menu: open it first
                t.locator("#lang-switch button[name=lang][value=id]").click()
                expect(t).to_have_url(base_url + "/admin/login")
                check_page(t, "login", "Sign-in page after one press of Indonesia")
                teacher_login(t, base_url, admin_email, "Guru Wali")
                check_page(t, "exams", "Exams list after sign-in")
                if not phone:
                    nav = t.locator(".admin-header nav")
                    one_row({k: box(nav.locator(s)) for k, s in (("brand", ".brand"), ("links", ".nav-links"), ("email", "#signed-in-as"), ("logout", ".logout-form button"))})
                exam_id = import_exam(t, write_exam_zip(Path(tmp) / pass_name, title))
                check_page(t, "exam-imported", "Exam page right after the import (closed)")
                open_exam(t, exam_id, open_word=OPEN_WORD)
                check_page(t, "exams-open", "Exams list with the exam open")
                t.goto(f"{base_url}/admin/exams/{exam_id}")
                code = t.locator("#exam-code").inner_text().strip()
                check_page(t, "exam-open", "Exam page of the open exam with its code")
                t.locator("#projector-link").click()
                expect(t.locator("#projector-code")).to_have_text(code)
                check_page(t, "projector", "Projector view")

            # 2. Student in id-ID: /, /?code=, a refused start, start, answer, submit.
            s = new_context(locale="id-ID", **context_args).new_page()
            with step(f"{pass_name} 2a / and /?code= in Indonesian without a press: Start above the fold, the tabs, the boxes, the hero"):
                s.goto(base_url + "/")
                check_page(s, "landing", "Start page /")
                if phone:
                    start_bottom = bottom_of(s.locator("#start-form button[type=submit]"))
                    if start_bottom > viewport["height"]:
                        raise AssertionError(f"Start ends at {start_bottom:.1f} px, want at most {viewport['height']}")
                    tabs = s.locator(".entry-modes .tab")
                    expect(tabs).to_have_count(2)
                    if abs(bottom_of(tabs.nth(0)) - bottom_of(tabs.nth(1))) > 1:
                        raise AssertionError(f"the tabs end at {bottom_of(tabs.nth(0)):.1f} and {bottom_of(tabs.nth(1)):.1f}, want one row")
                    for i in range(2):
                        ends_inside(tabs.nth(i), viewport["width"], f"tab {i}")
                    code_box, name_box = box(s.locator("#start-code")), box(s.locator("#name"))
                    if abs(code_box["width"] - name_box["width"]) > 0.5 or {round(code_box["height"]), round(name_box["height"])} != {BOX}:
                        raise AssertionError(f"#start-code is {code_box['width']:.1f} × {code_box['height']:.1f}, #name {name_box['width']:.1f} × {name_box['height']:.1f}; want equal widths, {BOX} px high")
                    measured.setdefault("start /", []).append(f"{pass_name} {start_bottom:.1f}")
                else:
                    height = box(s.locator(".landing-split"))["height"]
                    if abs(height - viewport["height"]) > 1:
                        raise AssertionError(f".landing-split is {height:.1f} px high, want the window's {viewport['height']}")
                    board, column = box(s.locator("main#landing")), box(s.locator(".entry-col"))
                    off = (column["y"] + column["height"] / 2) - (board["y"] + board["height"] / 2)
                    if abs(off) > 2:
                        raise AssertionError(f"the student column's centre is {off:+.1f} px from the board's centre, want within 2")
                    measured.setdefault("hero", []).append(f"{pass_name} {height:.1f} px, column {off:+.1f} px off centre")
                s.goto(f"{base_url}/?code={code}")
                check_page(s, "landing-code", "Start page from the exam link /?code=")
                if phone:
                    code_bottom = bottom_of(s.locator("#start-form button[type=submit]"))
                    if code_bottom > viewport["height"]:
                        raise AssertionError(f"on /?code= Start ends at {code_bottom:.1f} px, want at most {viewport['height']}")
                    for label in (".tab-join", ".tab-resume"):
                        ends_inside(s.locator(f".entry-modes {label}"), viewport["width"], label)
                    measured.setdefault("start /?code=", []).append(f"{pass_name} {code_bottom:.1f}")

            with step(f"{pass_name} 2b without scripts: / and /?code= keep Start above the fold"):
                nj = new_context(locale="id-ID", java_script_enabled=False, **context_args).new_page()
                for where, url in (("/", base_url + "/"), ("/?code=", f"{base_url}/?code={code}")):
                    nj.goto(url)
                    expect(nj.locator("html")).to_have_attribute("lang", "id")
                    no_page_overflow(nj)
                    if phone:
                        bottom = bottom_of(nj.locator("#start-form button[type=submit]"))
                        if bottom > viewport["height"]:
                            raise AssertionError(f"without scripts on {where} Start ends at {bottom:.1f} px, want at most {viewport['height']}")
                        measured.setdefault(f"start {where} without scripts", []).append(f"{pass_name} {bottom:.1f}")
                    else:
                        height = box(nj.locator(".landing-split"))["height"] if where == "/" else viewport["height"]
                        if abs(height - viewport["height"]) > 1:
                            raise AssertionError(f"without scripts .landing-split is {height:.1f} px high, want {viewport['height']}")
                check_page(nj, "landing-code-noscript", "Start page from the exam link, without scripts")
                nj.context.close()

            with step(f"{pass_name} 2c a refused start (unknown code)"):
                s.goto(base_url + "/")
                form = s.locator("#start-form")
                form.locator("#start-code").fill(REFUSED_CODE)
                form.locator("#name").fill(STUDENT)
                form.locator("button[type=submit]").click()
                expect(s.locator("#start-alert")).to_be_visible()
                check_page(s, "start-refused", "Start refused: no open exam has that code")

            with step(f"{pass_name} 2d start from the link; the exam page and its bar"):
                s.goto(f"{base_url}/?code={code}")
                s.locator("#name").fill(STUDENT)
                s.locator("#start-form button[type=submit]").click()
                expect(s.locator("main#exam")).to_be_visible()
                expect(s.locator(".section-head")).to_have_count(2)
                options = s.locator("label.option")
                for i in range(options.count()):
                    at_least(options.nth(i), OPTION, f"label.option {i}")
                bar = ["#timer", "#save-state"] + (["#nav-toggle"] if phone else [])
                if phone:
                    expect(s.locator("#nav-toggle")).to_be_visible()
                    for hook in bar:
                        in_window(s.locator(hook), viewport, hook)
                    one_row({hook: box(s.locator(hook)) for hook in bar})
                check_page(s, "exam", "Exam page, nothing answered yet")

            with step(f"{pass_name} 2e answer everything; the longest save state (retrying) stays on one line"):
                pick(s, 1, PICKS[1])
                wait_saved(s)
                pick(s, 2, PICKS[2])
                wait_saved(s)
                retrying = s.locator("#exam-text").get_attribute("data-retrying")
                s.route("**/exam/answer", lambda route: route.abort())
                pick(s, 3, PICKS[3])
                expect(s.locator("#save-state")).to_have_text(retrying)
                one_line(s.locator("#save-state"), 1.5, f"#save-state reading {retrying!r}")
                widths = {hook: box(s.locator(hook))["width"] for hook in bar}
                if phone:
                    for hook in bar:
                        in_window(s.locator(hook), viewport, f"{hook} while retrying")
                    one_row({hook: box(s.locator(hook)) for hook in bar})
                    measured.setdefault("exam bar widths while retrying", []).append(f"{pass_name} " + ", ".join(f"{k} {v:.1f}" for k, v in widths.items()))
                shot(s, "exam-retrying", "Exam page, the save state while a save is retried", full=False)
                s.unroute("**/exam/answer")
                wait_saved(s, 20_000)
                s.locator("section.question#q4 input.answer-text").fill(TYPED)
                wait_saved(s)
                check_page(s, "exam-answered", "Exam page with every question answered")

            with step(f"{pass_name} 2f the picks and the typed answer survive a reload"):
                s.reload()
                expect(s.locator("main#exam")).to_be_visible()
                for n, option in PICKS.items():
                    if (got := picked(s, n)) != option:
                        raise AssertionError(f"after reload question {n} has {got!r}, want {option!r}")
                expect(s.locator("section.question#q4 input.answer-text")).to_have_value(TYPED)

            with step(f"{pass_name} 2g one blocked copy: the monitor banner fits and moves no question"):
                before = question_tops(s)
                copy_question(s)
                banner = s.locator("#monitor-banner")
                expect(banner).to_be_visible()
                after = question_tops(s)
                if any(abs(a - b) > 1 for a, b in zip(before, after)):
                    raise AssertionError(f"questions moved: tops {before} → {after}")
                banner.scroll_into_view_if_needed()
                in_window(banner, viewport, "#monitor-banner")
                shot(s, "exam-banner", "Exam page, the monitor banner after a blocked copy", full=False)
                s.evaluate("window.scrollTo(0, 0)")

            if phone:
                with step(f"{pass_name} 2h the navigator at 360 px"):
                    s.locator("#nav-toggle").click()
                    expect(s.locator("#question-nav")).to_have_attribute("data-open", "true")
                    expect(s.locator("#question-nav")).to_be_visible()
                    no_page_overflow(s)
                    if clipped := s.evaluate(CLIPPED_JS):
                        raise AssertionError("clipped with the navigator open:\n    " + "\n    ".join(clipped))
                    shot(s, "exam-navigator", "Exam page with the question navigator open", full=False)
                    s.locator("#nav-toggle").click()
                    expect(s.locator("#question-nav")).to_have_attribute("data-open", "false")

            with step(f"{pass_name} 2i the submit dialog fits; submit; the result with the review"):
                s.locator("#submit-open").click()
                dialog = s.locator("#submit-dialog")
                expect(dialog).to_be_visible()
                in_window(dialog, viewport, "#submit-dialog")
                for hook in ("#submit-cancel", "#submit-confirm"):
                    expect(s.locator(hook)).to_be_visible()
                    in_window(s.locator(hook), viewport, hook)
                check_page(s, "exam-dialog-full", "Exam page with the submit dialog (full page)", switch=False)
                shot(s, "exam-dialog", "Exam page with the submit dialog (window)", full=False)
                s.locator("#submit-confirm").click()
                expect(s.locator("main#result")).to_be_visible()
                for hook in (".score-block", ".result-home a"):
                    ends_inside(s.locator(hook), viewport["width"], hook)
                expect(s.locator(".review-fill-in")).to_have_attribute("data-correct", "true")
                expect(s.locator(".review-fill-in .typed-text")).to_have_text(TYPED)
                result_score = s.locator("#score").inner_text().strip()
                if result_score != "4 / 4":
                    raise AssertionError(f"the result shows {result_score}, want 4 / 4 (1003 against 1.003 is right)")
                check_page(s, "result", "Result page with the review")

            # 3. A second student stays in progress; a third resumes after a refused code.
            with step(f"{pass_name} 3 a second student stays in progress; a third is refused a wrong resume code, then resumes"):
                second = new_context(locale="id-ID", **context_args).new_page()
                second.goto(f"{base_url}/?code={code}")
                second.locator("#name").fill(SECOND)
                second.locator("#start-form button[type=submit]").click()
                expect(second.locator("main#exam")).to_be_visible()
                pick(second, 1, "32")
                wait_saved(second)
                third_context = new_context(locale="id-ID", **context_args)
                third = third_context.new_page()
                third.goto(f"{base_url}/?code={code}")
                third.locator("#name").fill(THIRD)
                third.locator("#start-form button[type=submit]").click()
                expect(third.locator("main#exam")).to_be_visible()
                third_context.close()
                t.goto(f"{base_url}/admin/exams/{exam_id}/attempts")
                resume_code = t.locator("#attempts tbody tr").filter(has_text=THIRD).locator("code.resume-code").inner_text().strip()
                back = new_context(locale="id-ID", **context_args).new_page()
                back.goto(base_url + "/")
                show_resume(back)
                back.locator("#code").fill(WRONG_RESUME)
                back.locator("#resume-form button[type=submit]").click()
                expect(back.locator("#panel-resume [role=alert]")).to_be_visible()
                check_page(back, "resume-refused", "Resume refused: a wrong resume code")
                back.locator("#code").fill("")  # the mask takes no insertion into a full box
                back.locator("#code").fill(resume_code)
                expect(back.locator("#code")).to_have_value(resume_code)
                back.locator("#resume-form button[type=submit]").click()
                expect(back.locator("main#exam")).to_be_visible()
                expect(back.locator("html")).to_have_attribute("lang", "id")

            # 4. Teacher: results, attempt, refusals, History, a copy and its editor, Teachers, 404, Log out.
            attempts_url = f"{base_url}/admin/exams/{exam_id}/attempts"
            with step(f"{pass_name} 4a results: the name filter, a sort, the same score as the result page"):
                t.goto(attempts_url)
                expect(t.locator("#attempts tbody tr")).to_have_count(3)
                check_page(t, "results", "Results: one submitted, two in progress")
                if not phone:
                    wide = t.locator("#results-live .overflow-auto").evaluate("el => [el.scrollWidth, el.clientWidth]")
                    if wide[0] > wide[1]:
                        raise AssertionError(f"the results table needs a sideways scroll: scrollWidth {wide[0]} > clientWidth {wide[1]}")
                row = t.locator("#attempts tbody tr").filter(has_text=STUDENT)
                if (row_score := row.locator("td").nth(4).inner_text().strip()) != result_score:
                    raise AssertionError(f"the results row shows {row_score}, the result page {result_score}")
                t.locator("#filter-name").fill(STUDENT.split()[0].lower())
                t.locator("#name-filter button[type=submit]").click()
                expect(t).to_have_url(re.compile(r"[?&]name="))
                expect(t.locator("#attempts tbody tr")).to_have_count(1)
                t.locator("#attempts thead a.sort-link").nth(1).click()
                expect(t).to_have_url(re.compile(r"sort=score"))
                expect(t.locator("th[aria-sort]")).to_have_count(1)
                check_page(t, "results-filtered", "Results filtered by name, sorted by score")

            with step(f"{pass_name} 4b the CSV: Indonesian header and status words, the same score"):
                with t.expect_download() as info:
                    t.locator("#export-csv").click()
                path = Path(tmp) / f"{pass_name}.csv"
                info.value.save_as(path)
                table = read_csv(path)
                want = CSV_HEAD + [SECTION_A, SECTION_B] + CSV_TAIL
                if table[0] != want:
                    raise AssertionError(f"CSV header {table[0]}, want {want}")
                rows = {r[0]: r for r in table[1:]}
                if set(rows) != {STUDENT, SECOND, THIRD}:
                    raise AssertionError(f"CSV names {sorted(rows)}")
                statuses = {name: r[1] for name, r in rows.items()}
                if statuses != {STUDENT: CSV_SUBMITTED, SECOND: CSV_IN_PROGRESS, THIRD: CSV_IN_PROGRESS}:
                    raise AssertionError(f"CSV statuses {statuses}")
                if f"{rows[STUDENT][2]} / {rows[STUDENT][3]}" != result_score:
                    raise AssertionError(f"CSV score {rows[STUDENT][2:4]}, the result page {result_score}")

            with step(f"{pass_name} 4c the attempt page: flags, 1003 right by value, an override pressed and removed, a refused rename"):
                t.goto(attempts_url)
                t.locator("#attempts tbody tr").filter(has_text=STUDENT).locator("td.row-actions a").first.click()
                expect(t.locator("main.attempt-detail h1")).to_have_text(STUDENT)
                expect(t.locator("#events tr.event")).not_to_have_count(0)
                expect(t.locator("#q4 .typed-text")).to_have_text(TYPED)
                expect(t.locator("#q4 .auto-mark")).to_have_attribute("data-auto", "true")
                if (score := t.locator(".attempt-summary dd").first.inner_text().strip()) != result_score:
                    raise AssertionError(f"the attempt page shows {score}, the result page {result_score}")
                check_page(t, "attempt", "Attempt page: flags, answers, the typed answer marked right")
                t.locator("#q4 .override-form button[value=wrong]").click()
                expect(t.locator("#q4 .override-mark")).to_be_visible()
                check_page(t, "attempt-override", "Attempt page with an override (marked wrong)")
                t.locator("#q4 .override-form button[value=none]").click()
                expect(t.locator("#q4 .override-mark")).to_have_count(0)
                t.locator("#rename-name").fill("   ")
                t.locator("#rename-form button[type=submit]").click()
                expect(t.locator("main.attempt-detail [role=alert]")).to_be_visible()
                check_page(t, "rename-refused", "Attempt page: rename refused (empty name)")

            with step(f"{pass_name} 4d the delete confirmation, cancelled"):
                t.goto(attempts_url)
                t.locator("#attempts tbody tr").filter(has_text=STUDENT).locator("a.danger-link").click()
                expect(t.locator("#delete-attempt-form")).to_be_visible()
                check_page(t, "confirm-delete-attempt", "Confirmation: delete an attempt")
                t.locator("main#confirm a.button").click()
                expect(t).to_have_url(attempts_url)
                expect(t.locator("#attempts tbody tr")).to_have_count(3)

            with step(f"{pass_name} 4e History and one revision"):
                t.goto(f"{base_url}/admin/exams/{exam_id}")
                t.locator("#history-link").click()
                expect(t.locator("#revisions tbody tr")).not_to_have_count(0)
                check_page(t, "history", "History of the exam")
                t.locator(f"#revisions tbody tr a[href*='/revisions/']").first.click()
                expect(t.locator("main.revision-page")).to_be_visible()
                check_page(t, "revision", "One revision")

            with step(f"{pass_name} 4f a copy and its editor: question form with an added option, a refused save, section, settings, new exam, remove question cancelled"):
                t.goto(f"{base_url}/admin/exams/{exam_id}")
                t.locator("#copy-form button[type=submit]").click()
                expect(t).to_have_url(re.compile(r"/admin/exams/\d+$"))
                copy_id = int(re.search(r"/admin/exams/(\d+)$", t.url).group(1))
                if copy_id == exam_id:
                    raise AssertionError("Copy stayed on the same exam")
                copy_title = t.locator(".exam-head h1").inner_text().strip()
                own.append(copy_title)
                check_page(t, "copy", "Exam page of the copy")
                editor_url = f"{base_url}/admin/exams/{copy_id}/edit"
                t.goto(editor_url)
                expect(t.locator("main.editor-page")).to_be_visible()
                check_page(t, "editor", "Editor of the copy")
                t.locator("#q1 a.edit-question").click()
                form = t.locator("#question-form")
                rows = form.locator("textarea[name=option]").count()
                form.locator("button[name=do][value=add]").click()
                expect(form.locator("textarea[name=option]")).to_have_count(rows + 1)
                check_page(t, "question-add-option", "Question form after Add option")
                form.locator("#question-text").fill("")
                form.locator("#save").click()
                expect(t.locator("main [role=alert]")).to_be_visible()
                check_page(t, "question-refused", "Question form: save refused (no question text)")
                t.goto(editor_url)
                t.locator("#s1 a.edit-section").click()
                expect(t.locator("#section-form")).to_be_visible()
                check_page(t, "section", "Section form")
                t.goto(editor_url)
                t.locator("#settings-link").click()
                expect(t.locator("#settings-form")).to_be_visible()
                check_page(t, "settings", "Exam settings form")
                t.goto(f"{base_url}/admin/exams")
                t.locator("#new-exam").click()
                expect(t.locator("#new-exam-form")).to_be_visible()
                check_page(t, "new-exam", "New exam form")
                t.goto(editor_url)
                t.locator("#q3 a.remove-question").click()
                expect(t.locator("#remove-question-form")).to_be_visible()
                check_page(t, "confirm-remove-question", "Confirmation: remove a question")
                t.locator("main#confirm a.button").click()
                expect(t.locator("main.editor-page")).to_be_visible()
                expect(t.locator("section.preview-question")).to_have_count(len(QUESTIONS))

            with step(f"{pass_name} 4g archive the copy through its confirmation"):
                t.goto(f"{base_url}/admin/exams/{copy_id}")
                t.locator("#archive-link").click()
                expect(t.locator("#archive-form")).to_be_visible()
                check_page(t, "confirm-archive", "Confirmation: archive the copy")
                t.locator("#archive-form button[type=submit]").click()
                expect(t).to_have_url(re.compile(rf"/admin/exams/{copy_id}$"))
                expect(t.locator("#archived-note")).to_be_visible()
                check_page(t, "copy-archived", "Exam page of the archived copy")

            with step(f"{pass_name} 4h Teachers with a refused add"):
                t.locator("header a[href='/admin/teachers']").click()
                expect(t.locator("#add-teacher-form")).to_be_visible()
                check_page(t, "teachers", "Teachers")
                t.locator("#add-teacher-email").fill(REFUSED_EMAIL)
                t.locator("#add-teacher-form button[type=submit]").click()
                expect(t.locator("main [role=alert]")).to_be_visible()
                own.append(REFUSED_EMAIL)
                check_page(t, "teachers-refused", "Teachers: add refused (not a valid email)")

            with step(f"{pass_name} 4i a 404 under /admin and one outside; Log out"):
                for name, path in (("404-admin", "/admin/tidak-ada"), ("404", "/tidak-ada")):
                    if (status := t.goto(base_url + path).status) != 404:
                        raise AssertionError(f"{path} answered {status}, want 404")
                    check_page(t, name, f"Page not found ({path})")
                t.goto(f"{base_url}/admin/exams")
                t.locator("form[action='/admin/logout'] button[type=submit]").click()
                expect(t).to_have_url(re.compile(r"/admin/login$"))
                check_page(t, "logged-out", "Sign-in page after Log out")

            for page in (t, s, second, back):
                page.context.close()

        with step("5 no console errors but the refused loads made on purpose, no page errors or foreign requests"):
            allowed = [
                re.compile(rf"^{re.escape(base_url)}{path}(\?.*)?: Failed to load resource: the server responded with a status of {status} ")
                for path, status, _ in EXEMPT
            ] + [re.compile(rf"^{re.escape(base_url)}{ABORTED}$")]
            problems = []
            for log in logs:
                problems += [f"console error: {e}" for e in log["console_errors"] if not any(a.match(e) for a in allowed)]
                problems += [f"page error: {e}" for e in log["page_errors"]]
                problems += [f"foreign request: {u}" for u in foreign_requests(log["requests"], base_url, oidc_url)]
            if problems:
                raise AssertionError("\n  ".join(["problems found:"] + problems))

        browser.close()

    lines = ["# Indonesian walk-through screenshots", ""]
    for pass_name, shots in index.items():
        lines += [f"## {pass_name}", "", "| File | Shows |", "| --- | --- |"]
        lines += [f"| `{pass_name}/{file}` | {what} |" for file, what in shots]
        lines.append("")
    (out / "index.md").write_text("\n".join(lines), encoding="utf-8")
    return measured


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True)
    add_sign_in_args(parser)
    parser.add_argument("--out", type=Path, default=Path(__file__).parent / "out" / "walkthrough-id")
    args = parser.parse_args()
    base_url, oidc_url = args.base_url.rstrip("/"), args.oidc_url.rstrip("/")
    require_cross_site(base_url, oidc_url)
    try:
        measured = run(base_url, args.admin_email, oidc_url, args.out)
    except StepFailed as err:
        print(err, file=sys.stderr)
        return 1
    for what, values in measured.items():
        print(f"  {what}: {'; '.join(values)}")
    print(f"OK: Indonesian walk-through passed in {len(PASSES)} passes; screenshots in {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
