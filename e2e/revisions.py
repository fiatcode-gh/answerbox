# /// script
# requires-python = ">=3.12"
# dependencies = ["playwright==1.63.0"]
# ///
"""Browser check for exam revisions in real Chromium: imports and replaces
make numbered revisions, each revision opens read-only with its own title and
image, a downloaded exam imports again to an equal exam (with and without
images), Copy exam makes a closed exam with a new code, and an attempt shows
the revision it started on. Needs a fresh instance.

uv run e2e/revisions.py --base-url URL --admin-email EMAIL --oidc-url URL --out DIR
"""

import argparse
import re
import sys
import tempfile
from contextlib import contextmanager
from pathlib import Path

from playwright.sync_api import Page, expect, sync_playwright

from common import (
    EXAM_CODE,
    add_sign_in_args,
    build_fixture_zip,
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
# Title and image width of each revision of the source exam; every image is 120 px high.
VERSIONS = [("Geometry v1", 160), ("Geometry v2", 200), ("Geometry v3", 240)]
IMPORTED = "Imported"
REPLACED = "Replaced from file"
FROZEN = "This exam has attempts, so its questions are frozen."
STUDENT = "Ayu Lestari"
TEXT_ONLY = """# Spelling check
shuffle: none
show_result: score

1. Which word is spelled correctly?
- necessary *
- neccessary
- necesary

2. Which word is a **noun**?
- table *
- quickly
- blue
"""


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


def exam_page(page: Page, base_url: str, exam_id: int) -> str:
    """Open the exam's page; returns its code."""
    page.goto(f"{base_url}/admin/exams/{exam_id}")
    code = page.locator("#exam-code").inner_text().strip()
    if not EXAM_CODE.match(code):
        raise AssertionError(f"exam {exam_id} has code {code!r}, want 6 symbols of the code alphabet")
    return code


def history(page: Page, base_url: str, exam_id: int) -> list[list[str]]:
    """Open the exam's page, press History; returns the rows' cells (number, saved, teacher, summary)."""
    page.goto(f"{base_url}/admin/exams/{exam_id}")
    page.locator("#history-link").click()
    expect(page).to_have_url(re.compile(rf"/admin/exams/{exam_id}/revisions$"))
    expect(page.locator("#revisions")).to_be_visible()
    rows = page.locator("#revisions tbody tr[data-revision]")
    return [[c.strip() for c in rows.nth(i).locator("td").all_inner_texts()[:4]] for i in range(rows.count())]


def preview(page: Page) -> list[tuple[str, str]]:
    """(question text, correct option text) of every question on the page, in order."""
    questions = page.locator("section.preview-question")
    result = []
    for i in range(questions.count()):
        q = questions.nth(i)
        expect(q.locator("li[data-correct=true] .correct-mark")).to_have_text("✓ Correct")
        expect(q.locator(".correct-mark")).to_have_count(1)
        result.append(
            (q.locator(".question-body").inner_text().strip(), q.locator("li[data-correct=true] .option-body").inner_text().strip())
        )
    return result


def image_width(page: Page) -> int:
    """naturalWidth of the one question image, after it has loaded."""
    images = page.locator("section.preview-question img")
    expect(images).to_have_count(1)
    images.first.evaluate(
        "img => img.complete ? null : new Promise((ok, fail) => { img.onload = ok; img.onerror = fail; })"
    )
    return images.first.evaluate("img => img.naturalWidth")


def download(page: Page, base_url: str, exam_id: int, dir: Path) -> Path:
    """Press Download on the exam's page; returns the saved file, named as the server suggested."""
    page.goto(f"{base_url}/admin/exams/{exam_id}")
    with page.expect_download() as info:
        page.locator("#download-link").click()
    path = dir / info.value.suggested_filename
    info.value.save_as(path)
    return path


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

        a = new_context(**DESKTOP).new_page()
        with step("1 admin signs in; the instance is fresh"):
            teacher_login(a, base_url, admin_email, "Ada Admin")
            if a.get_by_text("No exams yet.").count() != 1:
                raise AssertionError("not a fresh instance: the exams page does not say 'No exams yet.'")

        fixtures = []
        for n, (title, width) in enumerate(VERSIONS, start=1):
            (tmp / f"v{n}").mkdir()
            fixtures.append(build_fixture_zip(tmp / f"v{n}", title=title, image_size=(width, 120)))

        with step("2 import Geometry v1; its history has revision 1 Imported by the admin"):
            source_id = import_exam(a, fixtures[0][0])
            rows = history(a, base_url, source_id)
            if len(rows) != 1:
                raise AssertionError(f"history has {len(rows)} rows, want 1: {rows}")
            number, saved, teacher, summary = rows[0]
            if (number, teacher, summary) != ("1", admin_email, IMPORTED) or not re.search(r"\d\d:\d\d", saved):
                raise AssertionError(f"history row {rows[0]}, want 1, a time, {admin_email}, {IMPORTED}")
            a.screenshot(path=out / "desktop-history-1.png", full_page=True, animations="disabled")

        with step("3 replace with v2 and v3; history lists 3, 2, 1 newest first"):
            for zip_path, _ in fixtures[1:]:
                a.goto(f"{base_url}/admin/exams/{source_id}")
                form = a.locator("#replace-form")
                form.locator("input[type=file][name=file]").set_input_files(str(zip_path))
                form.locator("button[type=submit]").click()
                expect(a).to_have_url(re.compile(rf"/admin/exams/{source_id}$"))
            rows = history(a, base_url, source_id)
            got = [(r[0], r[2], r[3]) for r in rows]
            want = [("3", admin_email, REPLACED), ("2", admin_email, REPLACED), ("1", admin_email, IMPORTED)]
            if got != want:
                raise AssertionError(f"history rows {got}, want {want}")
            a.screenshot(path=out / "desktop-history-3.png", full_page=True, animations="disabled")

        with step("4 each revision opens read-only with its own title, six keyed questions and its image"):
            for n, (title, width) in enumerate(VERSIONS, start=1):
                history(a, base_url, source_id)
                a.locator(f"#revisions tr[data-revision='{n}'] a").click()
                expect(a).to_have_url(re.compile(rf"/admin/exams/{source_id}/revisions/{n}$"))
                expect(a.locator("h1")).to_have_text(f"Revision {n}")
                expect(a.locator("dl.settings dd").first).to_have_text(title)
                expect(a.locator("section.preview-question")).to_have_count(6)
                key = fixtures[n - 1][1]
                got = {text.split("\n")[0]: correct for text, correct in preview(a)}
                if got != key:
                    raise AssertionError(f"revision {n} questions {got}, want {key}")
                expect(a.locator("main form")).to_have_count(0)
                got = image_width(a)
                if got != width:
                    raise AssertionError(f"revision {n} image is {got} px wide, want {width}")
                if n == 1:
                    a.screenshot(path=out / "desktop-revision-1.png", full_page=True, animations="disabled")
                    a.set_viewport_size(PHONE_VIEWPORT)
                    no_page_overflow(a)
                    a.screenshot(path=out / "phone-revision-1.png", full_page=True, animations="disabled")
                    a.set_viewport_size(DESKTOP["viewport"])

        with step("5 download Geometry v3 as exam-<code>.zip"):
            source_code = exam_page(a, base_url, source_id)
            source_preview = preview(a)
            a.screenshot(path=out / "desktop-exam-actions.png", full_page=False, animations="disabled")
            (tmp / "downloads").mkdir()
            zip_path = download(a, base_url, source_id, tmp / "downloads")
            if zip_path.name != f"exam-{source_code}.zip":
                raise AssertionError(f"download named {zip_path.name!r}, want exam-{source_code}.zip")

        with step("6 the downloaded zip imports again as an equal exam with revision 1 Imported"):
            a.goto(f"{base_url}/admin/exams")
            again_id = import_exam(a, zip_path)
            expect(a.locator(".exam-head h1")).to_have_text("Geometry v3")
            expect(a.locator("section.preview-question")).to_have_count(6)
            if preview(a) != source_preview:
                raise AssertionError(f"re-imported preview {preview(a)}, want {source_preview}")
            if (got := image_width(a)) != 240:
                raise AssertionError(f"re-imported image is {got} px wide, want 240")
            rows = history(a, base_url, again_id)
            if [r[3] for r in rows] != [IMPORTED]:
                raise AssertionError(f"re-imported history {rows}, want one row {IMPORTED}")

        with step("7 a text-only exam downloads as exam-<code>.md and imports again with the same preview"):
            md_path = tmp / "spelling.md"
            md_path.write_text(TEXT_ONLY)
            a.goto(f"{base_url}/admin/exams")
            text_id = import_exam(a, md_path)
            text_code = exam_page(a, base_url, text_id)
            text_preview = preview(a)
            text_md = download(a, base_url, text_id, tmp / "downloads")
            if text_md.name != f"exam-{text_code}.md":
                raise AssertionError(f"download named {text_md.name!r}, want exam-{text_code}.md")
            a.goto(f"{base_url}/admin/exams")
            import_exam(a, text_md)
            expect(a.locator(".exam-head h1")).to_have_text("Spelling check")
            if preview(a) != text_preview:
                raise AssertionError(f"re-imported preview {preview(a)}, want {text_preview}")

        with step("8 Copy exam makes a closed Geometry v3 with a new code and revision 1 Copied from"):
            a.goto(f"{base_url}/admin/exams/{source_id}")
            a.locator("#copy-form button[type=submit]").click()
            expect(a).not_to_have_url(re.compile(rf"/admin/exams/{source_id}$"))
            expect(a).to_have_url(re.compile(r"/admin/exams/\d+$"))
            copy_id = int(re.search(r"/admin/exams/(\d+)$", a.url).group(1))
            expect(a.locator(".exam-head .badge")).to_have_text("Closed")
            expect(a.locator(".exam-head h1")).to_have_text("Geometry v3")
            copy_code = a.locator("#exam-code").inner_text().strip()
            if not EXAM_CODE.match(copy_code) or copy_code == source_code:
                raise AssertionError(f"copy has code {copy_code!r}, source has {source_code!r}")
            if preview(a) != source_preview:
                raise AssertionError(f"copy preview {preview(a)}, want {source_preview}")
            if (got := image_width(a)) != 240:
                raise AssertionError(f"copy image is {got} px wide, want 240")
            a.screenshot(path=out / "desktop-copy.png", full_page=True, animations="disabled")
            rows = history(a, base_url, copy_id)
            want = f"Copied from Geometry v3 ({source_code}), revision 3"
            if [(r[0], r[3]) for r in rows] != [("1", want)]:
                raise AssertionError(f"copy history {rows}, want one row 1 {want!r}")
            if len(history(a, base_url, source_id)) != 3:
                raise AssertionError("the source's history no longer has three rows")

        s = new_context(**phone).new_page()
        with step("9 a student's attempt shows Revision 3; the exam is frozen and keeps Download and Copy"):
            a.goto(f"{base_url}/admin/exams/{source_id}")
            open_exam(a, source_id)
            start(s, base_url, source_code, STUDENT)
            expect(s.locator(".exam-title")).to_have_text("Geometry v3")
            s.locator("#submit-open").click()
            expect(s.locator("#submit-dialog")).to_be_visible()
            s.locator("#submit-confirm").click()
            expect(s.locator("main#result")).to_be_visible()
            a.goto(f"{base_url}/admin/exams/{source_id}/attempts")
            a.locator("#attempts tbody tr[data-attempt-id]").filter(has_text=STUDENT).get_by_role("link", name="View").click()
            expect(a).to_have_url(re.compile(rf"/admin/exams/{source_id}/attempts/\d+$"))
            expect(a.locator("#attempt-revision")).to_have_text("Revision 3")
            a.screenshot(path=out / "desktop-attempt.png", full_page=True, animations="disabled")
            a.locator("#attempt-revision a").click()
            expect(a).to_have_url(re.compile(rf"/admin/exams/{source_id}/revisions/3$"))
            expect(a.locator("h1")).to_have_text("Revision 3")
            a.goto(f"{base_url}/admin/exams/{source_id}")
            expect(a.locator("section.replace .frozen-notice")).to_have_text(FROZEN)
            expect(a.locator("#replace-form")).to_have_count(0)
            expect(a.locator("#download-link")).to_be_visible()
            expect(a.locator("#copy-form button[type=submit]")).to_be_visible()
            a.screenshot(path=out / "desktop-frozen.png", full_page=True, animations="disabled")

        with step("10 no console errors, page errors or foreign requests"):
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
    parser.add_argument("--out", type=Path, default=Path(__file__).parent / "out" / "revisions")
    args = parser.parse_args()
    base_url, oidc_url = args.base_url.rstrip("/"), args.oidc_url.rstrip("/")
    require_cross_site(base_url, oidc_url)
    try:
        run(base_url, args.admin_email, oidc_url, args.out)
    except StepFailed as err:
        print(err, file=sys.stderr)
        return 1
    print(f"OK: revisions check passed; screenshots in {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
