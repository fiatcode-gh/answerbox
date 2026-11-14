# /// script
# requires-python = ">=3.12"
# dependencies = ["playwright==1.63.0"]
# ///
"""Browser smoke for the student exam page (exam.js) in real Chromium.

uv run e2e/student_ui.py --base-url URL --admin-password PW --out DIR
"""

import argparse
import re
import sys
import tempfile
import time
from contextlib import contextmanager
from pathlib import Path

from playwright.sync_api import Page, expect, sync_playwright

from common import build_fixture_zip, import_and_open, teacher_login, watch

DESKTOP = {"viewport": {"width": 1280, "height": 800}}


class StepFailed(Exception):
    pass


@contextmanager
def step(name: str):
    print(f"step: {name}", flush=True)
    try:
        yield
    except Exception as err:  # every failure names its step
        raise StepFailed(f"FAIL [{name}]: {err}") from err


def seconds(timer: str) -> int:
    m, s = timer.split(":")
    return int(m) * 60 + int(s)


def start(page: Page, base_url: str, name: str) -> None:
    page.goto(base_url + "/")
    form = page.locator("#start-form")
    form.locator("input[name=name]").fill(name)
    form.locator("button[type=submit]").click()
    expect(page.locator("main#exam")).to_be_visible()


def question(page: Page, n: int):
    return page.locator(f"section.question#q{n}")


def pick(page: Page, n: int, index: int) -> str:
    """Pick option index of question n by its label; returns the option value."""
    label = question(page, n).locator("label.option").nth(index)
    value = label.locator("input[type=radio]").get_attribute("value")
    label.click()
    return value


def page_state(page: Page) -> list[tuple[str, str | None]]:
    """(question id, checked option value) per question, in page order."""
    return page.eval_on_selector_all(
        "section.question",
        """sections => sections.map(s => [
            s.dataset.questionId,
            (s.querySelector("input[type=radio]:checked") || {}).value || null,
        ])""",
    )


def run(base_url: str, password: str, out: Path) -> None:
    out.mkdir(parents=True, exist_ok=True)
    logs = []
    with sync_playwright() as p, tempfile.TemporaryDirectory() as tmp:
        browser = p.chromium.launch(headless=True)
        phone = p.devices["Pixel 7"]

        def new_context(**kwargs):
            context = browser.new_context(**kwargs)
            logs.append(watch(context, base_url))
            return context

        with step("1 teacher imports and opens the exam"):
            teacher = new_context(**DESKTOP).new_page()
            zip_path, _ = build_fixture_zip(tmp)
            teacher_login(teacher, base_url, password)
            import_and_open(teacher, zip_path)

        s1_context = new_context(**phone)
        s1 = s1_context.new_page()
        with step("2 S1 starts; timer counts down; navigator closed"):
            start(s1, base_url, "Ayu Lestari")
            timer = s1.locator("#timer")
            expect(timer).to_have_text(re.compile(r"^\d\d:\d\d$"))
            before = seconds(timer.inner_text())
            time.sleep(2.1)
            after = seconds(timer.inner_text())
            if not after < before:
                raise AssertionError(f"timer did not count down: {before}s then {after}s")
            expect(s1.locator("#nav-toggle")).to_be_visible()
            expect(s1.locator("#nav-toggle")).to_have_attribute("aria-expanded", "false")
            expect(s1.locator("#question-nav")).to_be_hidden()

        with step("3 S1 picks in question 1 and it is saved"):
            pick(s1, 1, 0)
            expect(s1.locator("#save-state")).to_have_attribute("data-state", "saved")
            expect(question(s1, 1).locator(".q-state")).to_have_attribute("data-state", "saved")
            expect(s1.locator("#question-nav a.bubble[href='#q1']")).to_have_attribute("data-answered", "true")
            expect(s1.locator("#answered-count")).to_have_text("Answered 1 of 6")
            s1.screenshot(path=out / "phone-exam.png", animations="disabled")

        with step("4 S1 offline pick shows Not saved, then saves when back online"):
            s1_context.set_offline(True)
            pick(s1, 2, 1)
            expect(s1.locator("#save-state")).to_have_attribute("data-state", "error", timeout=15_000)
            expect(s1.locator("#save-state")).to_have_text("✗ Not saved — retrying")
            expect(question(s1, 2).locator(".q-state")).to_have_text("Not saved")
            s1.screenshot(path=out / "phone-exam-not-saved.png", animations="disabled")
            s1_context.set_offline(False)
            expect(s1.locator("#save-state")).to_have_attribute("data-state", "saved", timeout=20_000)
            expect(question(s1, 2).locator(".q-state")).to_have_attribute("data-state", "saved")

        with step("5 S1 changes question 1; reload keeps picks and order"):
            pick(s1, 1, 1)
            expect(s1.locator("#save-state")).to_have_attribute("data-state", "saved")
            expect(question(s1, 1).locator(".q-state")).to_have_attribute("data-state", "saved")
            before = page_state(s1)
            s1.reload()
            expect(s1.locator("main#exam")).to_be_visible()
            after = page_state(s1)
            if after != before:
                raise AssertionError(f"page changed on reload: {before} → {after}")
            if [v is not None for _, v in after] != [True, True, False, False, False, False]:
                raise AssertionError(f"unexpected picks after reload: {after}")

        with step("6 S1 navigator opens and a bubble jumps to question 5"):
            s1.locator("#nav-toggle").tap()
            expect(s1.locator("#question-nav")).to_have_attribute("data-open", "true")
            expect(s1.locator("#question-nav")).to_be_visible()
            expect(s1.locator("#nav-toggle")).to_have_attribute("aria-expanded", "true")
            s1.screenshot(path=out / "phone-navigator.png", animations="disabled")
            s1.locator("#question-nav a.bubble[href='#q5']").tap()
            expect(s1.locator("#question-nav")).to_have_attribute("data-open", "false")
            expect(s1.locator("#question-nav")).to_be_hidden()
            expect(s1.locator("#nav-toggle")).to_have_attribute("aria-expanded", "false")
            expect(question(s1, 5)).to_be_in_viewport()

        with step("7 S1 submit dialog summarises and Keep working closes it"):
            s1.locator("#submit-open").click()
            expect(s1.locator("#submit-dialog")).to_be_visible()
            expect(s1.locator("#submit-summary")).to_have_text(
                "You answered 2 of 6 questions. 4 questions have no answer. "
                "You can't change answers after submitting."
            )
            expect(s1.locator("#submit-confirm")).to_have_text("Submit")
            expect(s1.locator("#submit-confirm")).to_be_enabled()
            s1.screenshot(path=out / "phone-submit-dialog.png", animations="disabled")
            s1.locator("#submit-cancel").click()
            expect(s1.locator("#submit-dialog")).to_be_hidden()
            expect(s1.locator("main#exam")).to_be_visible()

        with step("8 S2 is submitted automatically when the countdown ends"):
            s2 = new_context(**phone).new_page()
            s2.clock.install()
            start(s2, base_url, "Budi Santoso")
            pick(s2, 1, 0)
            expect(s2.locator("#save-state")).to_have_attribute("data-state", "saved")
            expect(question(s2, 1).locator(".q-state")).to_have_attribute("data-state", "saved")
            s2.clock.fast_forward("01:00:01")
            expect(s2.locator("main#result")).to_be_visible(timeout=15_000)
            expect(s2.locator("main#result")).to_have_attribute("data-status", "submitted")

        with step("9 S3 on desktop sees the navigator as a left column"):
            s3 = new_context(**DESKTOP).new_page()
            start(s3, base_url, "Citra Dewi")
            expect(s3.locator("#question-nav")).to_be_visible()
            expect(s3.locator("#nav-toggle")).to_be_hidden()
            nav = s3.locator("#question-nav").bounding_box()
            first = question(s3, 1).bounding_box()
            if not nav["x"] + nav["width"] <= first["x"]:
                raise AssertionError(f"navigator is not left of the questions: nav={nav} q1={first}")
            s3.screenshot(path=out / "desktop-exam.png", animations="disabled")

        with step("10 no console errors, page errors or foreign requests"):
            problems = []
            for log in logs:
                # Chromium itself logs the requests step 4 makes while offline on
                # purpose; that is the only state in which this message can occur.
                problems += [
                    f"console error: {e}"
                    for e in log["console_errors"]
                    if not e.endswith("Failed to load resource: net::ERR_INTERNET_DISCONNECTED")
                ]
                problems += [f"page error: {e}" for e in log["page_errors"]]
                problems += [f"foreign request: {u}" for u in log["requests"] if not u.startswith(base_url)]
            if problems:
                raise AssertionError("\n  ".join(["problems found:"] + problems))

        browser.close()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--admin-password", required=True)
    parser.add_argument("--out", type=Path, default=Path(__file__).parent / "out")
    args = parser.parse_args()
    try:
        run(args.base_url.rstrip("/"), args.admin_password, args.out)
    except StepFailed as err:
        print(err, file=sys.stderr)
        return 1
    print(f"OK: student UI smoke passed; screenshots in {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
