# /// script
# requires-python = ">=3.12"
# dependencies = ["playwright==1.63.0"]
# ///
"""End-to-end browser run of go-exam (teacher and students) against a fresh instance.

uv run e2e/exam_flow.py --base-url URL --admin-password PW --out DIR
"""

import argparse
import csv
import io
import json
import re
import sys
import tempfile
import time
from contextlib import contextmanager
from pathlib import Path

from playwright.sync_api import BrowserContext, Page, expect, sync_playwright

from common import build_fixture_zip, import_and_open, import_exam, open_exam, teacher_login, watch

DESKTOP = {"viewport": {"width": 1280, "height": 800}}
TIMER = re.compile(r"^\d\d:\d\d$")
RESUME_CODE = re.compile(r"^[A-Z2-9]{4}-[A-Z2-9]{4}$")
AYU = "Ayu Lestari Kusumawardhani Putri Maharani"
BUDI = "Budi Santoso Wijayakusuma Adiningrat Pratama"


class StepFailed(Exception):
    pass


def seconds(timer: str) -> int:
    m, s = timer.split(":")
    return int(m) * 60 + int(s)


def start(page: Page, base_url: str, name: str) -> None:
    page.goto(base_url + "/")
    form = page.locator("#start-form")
    form.locator("input[name=name]").fill(name)
    form.locator("button[type=submit]").click()
    expect(page.locator("main#exam")).to_be_visible()


def page_state(page: Page) -> list[list]:
    """[question id, checked option value or None] per question, in page order."""
    return page.eval_on_selector_all(
        "section.question",
        """sections => sections.map(s => [
            s.dataset.questionId,
            (s.querySelector("input[type=radio]:checked") || {}).value || null,
        ])""",
    )


def answer(page: Page, key: dict[str, str], plan: list[bool]) -> list[tuple[str, str]]:
    """Answer the first len(plan) questions in page order: True picks the correct
    option, False the first wrong one. Waits until every answer is saved.

    Returns (question text, picked option text) per answered question.
    """
    picks = []
    sections = page.locator("section.question")
    for n, correct in enumerate(plan):
        section = sections.nth(n)
        body = section.locator(".question-body").inner_text()
        text = next(q for q in key if q in body)
        labels = section.locator("label.option")
        for i in range(labels.count()):
            option = labels.nth(i).locator(".option-body").inner_text().strip()
            if (option == key[text]) == correct:
                labels.nth(i).click()
                picks.append((text, option))
                break
        expect(section.locator(".q-state")).to_have_attribute("data-state", "saved")
    expect(page.locator("#save-state")).to_have_attribute("data-state", "saved")
    return picks


def submit(page: Page, summary: str | re.Pattern) -> None:
    page.locator("#submit-open").click()
    expect(page.locator("#submit-dialog")).to_be_visible()
    expect(page.locator("#submit-summary")).to_have_text(summary)


def resume(page: Page, base_url: str, code: str) -> None:
    page.goto(base_url + "/")
    page.locator("details.resume summary").click()
    form = page.locator("#resume-form")
    form.locator("input[name=code]").fill(code)
    form.locator("button[type=submit]").click()


def attempt_row(page: Page, name: str):
    return page.locator("#attempts tbody tr[data-attempt-id]").filter(has_text=name)


def no_page_overflow(page: Page) -> None:
    widths = page.evaluate(
        "() => [document.documentElement.scrollWidth, document.documentElement.clientWidth]"
    )
    if widths[0] > widths[1]:
        raise AssertionError(f"page overflows horizontally on {page.url}: scrollWidth {widths[0]} > clientWidth {widths[1]}")


def attempts_table_fits(page: Page) -> None:
    m = page.evaluate(
        """() => {
            const box = document.querySelector("main.attempts-page .overflow-auto");
            return {
                column: document.querySelector("main.attempts-page").getBoundingClientRect().width,
                scroll: box.scrollWidth,
                client: box.clientWidth,
                right: box.getBoundingClientRect().right,
                deletes: [...document.querySelectorAll("#attempts .row-actions .danger-link")]
                    .map(a => a.getBoundingClientRect().right),
            };
        }"""
    )
    if m["column"] != 1100:
        raise AssertionError(f"page column is {m['column']} px wide, want 1100")
    if m["scroll"] > m["client"]:
        raise AssertionError(f"attempts table scrolls sideways: needs {m['scroll']} px, has {m['client']}")
    if not m["deletes"] or any(r > m["right"] + 0.5 for r in m["deletes"]):
        raise AssertionError(f"a Delete link is clipped: right edges {m['deletes']}, box ends at {m['right']}")


def download_csv(page: Page) -> list[list[str]]:
    with page.expect_download() as info:
        page.locator("#export-csv").click()
    return list(csv.reader(io.StringIO(Path(info.value.path()).read_text(encoding="utf-8"))))


class Run:
    def __init__(self, base_url: str, out: Path):
        self.base_url = base_url
        self.out = out
        self.steps: list[dict] = []
        self.screenshots: list[str] = []
        self.logs: list[dict] = []

    @contextmanager
    def step(self, name: str):
        print(f"step: {name}", flush=True)
        try:
            yield
        except Exception as err:  # every failure names its step
            self.steps.append({"name": name, "ok": False})
            raise StepFailed(f"FAIL [{name}]: {err}") from err
        self.steps.append({"name": name, "ok": True})

    def shot(self, page: Page, name: str) -> None:
        # From the top, so sticky bars render in their place on a full-page capture.
        page.evaluate("window.scrollTo(0, 0)")
        page.screenshot(path=self.out / name, full_page=True, animations="disabled")
        self.screenshots.append(name)

    def expected_status_errors(self) -> set[str]:
        # Chromium logs every non-2xx document load as a console error. Steps 1
        # and 13 provoke exactly these two on purpose (a wrong password answers
        # 401, an invalid resume code 422); nothing else may appear.
        return {
            f"{self.base_url}/admin/login: Failed to load resource: "
            "the server responded with a status of 401 (Unauthorized)",
            f"{self.base_url}/resume: Failed to load resource: "
            "the server responded with a status of 422 (Unprocessable Entity)",
        }

    def report(self) -> dict:
        requests = [u for log in self.logs for u in log["requests"]]
        console = [e for log in self.logs for e in log["console_errors"]]
        expected = self.expected_status_errors()
        return {
            "base_url": self.base_url,
            "steps": self.steps,
            "screenshots": self.screenshots,
            "requests_total": len(requests),
            "external_requests": [u for u in requests if not u.startswith(self.base_url)],
            "console_errors": [e for e in console if e not in expected],
            "expected_status_errors": [e for e in console if e in expected],
            "page_errors": [e for log in self.logs for e in log["page_errors"]],
        }


def flow(run: Run, password: str) -> None:
    base_url = run.base_url
    step = run.step
    with sync_playwright() as p, tempfile.TemporaryDirectory() as tmp:
        browser = p.chromium.launch(headless=True)
        phone = p.devices["Pixel 7"]

        def new_context(**kwargs) -> BrowserContext:
            context = browser.new_context(**kwargs)
            run.logs.append(watch(context, base_url))
            return context

        teacher = new_context(**DESKTOP).new_page()
        geometry_dir = Path(tmp) / "geometry"
        geometry_dir.mkdir()
        geometry_zip, key = build_fixture_zip(geometry_dir, title="Geometry check", show_result="review")

        with step("1 teacher login: redirect, wrong password, right password"):
            teacher.goto(base_url + "/admin/exams")
            expect(teacher).to_have_url(re.compile(r"/admin/login$"))
            teacher.locator("#login-form input[name=password]").fill(password + "-wrong")
            teacher.locator("#login-form button[type=submit]").click()
            expect(teacher.locator("[role=alert]")).to_have_text("Wrong password.")
            teacher_login(teacher, base_url, password)
            if teacher.get_by_text("No exams yet.").count() != 1:
                raise AssertionError("not a fresh instance: the exams page does not say 'No exams yet.'")

        with step("2 teacher imports Geometry check: preview with key and image"):
            exam_id = import_exam(teacher, geometry_zip)
            expect(teacher.locator("section.preview-question")).to_have_count(6)
            expect(teacher.locator(".correct-mark").filter(has_text="✓ Correct")).to_have_count(6)
            teacher.wait_for_function(
                """() => { const i = document.querySelector("section.preview-question img");
                           return i && i.complete && i.naturalWidth > 0 }"""
            )
            run.shot(teacher, "desktop-admin-exam.png")

        with step("3 teacher opens the exam"):
            open_exam(teacher, exam_id)
            run.shot(teacher, "desktop-admin-exams.png")

        a = new_context(**phone).new_page()
        with step("4 student A sees the landing page and starts"):
            a.goto(base_url + "/")
            expect(a.locator("h1")).to_have_text("Geometry check")
            run.shot(a, "phone-landing.png")
            start(a, base_url, AYU)

        with step("5 student A answers all 6 (4 correct, 2 wrong)"):
            expect(a.locator("#timer")).to_have_text(TIMER)
            a_picks = answer(a, key, [True, True, True, True, False, False])
            a_wrong = a_picks[4:]
            expect(a.locator("#save-state")).to_have_attribute("data-state", "saved")
            run.shot(a, "phone-exam.png")

        with step("6 student A reloads: same picks, same order, lower timer"):
            before_state = page_state(a)
            before_timer = seconds(a.locator("#timer").inner_text())
            time.sleep(2.1)  # the timer must visibly advance across the reload
            a.reload()
            expect(a.locator("main#exam")).to_be_visible()
            expect(a.locator("input[type=radio]:checked")).to_have_count(6)
            after_state = page_state(a)
            if after_state != before_state:
                raise AssertionError(f"page changed on reload: {before_state} → {after_state}")
            after_timer = seconds(a.locator("#timer").inner_text())
            if not after_timer < before_timer:
                raise AssertionError(f"timer not lower after reload: {before_timer}s then {after_timer}s")

        with step("7 student A opens the submit dialog and confirms"):
            submit(a, "You answered 6 of 6 questions. You can't change answers after submitting.")
            run.shot(a, "phone-submit-dialog.png")
            a.locator("#submit-confirm").click()

        with step("8 student A sees the review result 4 / 6"):
            expect(a.locator('main#result[data-show="review"]')).to_be_visible()
            expect(a.locator("#score")).to_have_text("4 / 6")
            expect(a.locator('main#result [data-correct="true"]')).to_have_count(6)
            for text, option in a_wrong:
                picked = a.locator("section.review-question").filter(has_text=text).locator(
                    'li.review-option[data-picked="true"]'
                )
                expect(picked).to_have_attribute("data-correct", "false")
                expect(picked.locator(".option-body")).to_have_text(option)
                expect(picked.locator(".review-mark")).to_have_text("✗ Your answer")
            run.shot(a, "phone-result-review.png")

        b1_context = new_context(**phone)
        b1 = b1_context.new_page()
        with step("9 student B starts on phone context B1 and answers 2 (1 correct, 1 wrong)"):
            start(b1, base_url, BUDI)
            answer(b1, key, [True, False])
            b_state = page_state(b1)
            if [v is not None for _, v in b_state] != [True, True, False, False, False, False]:
                raise AssertionError(f"unexpected picks for B: {b_state}")

        attempts_url = f"{base_url}/admin/exams/{exam_id}/attempts"
        with step("10 teacher attempts list shows A submitted and B in progress with a resume code"):
            teacher.goto(attempts_url)
            expect(attempt_row(teacher, AYU)).to_contain_text("Submitted")
            expect(attempt_row(teacher, AYU)).to_contain_text("4 / 6")
            b_row = attempt_row(teacher, BUDI)
            expect(b_row).to_contain_text("In progress")
            expect(b_row.locator("td").nth(5)).to_have_text(TIMER)
            expect(b_row.locator("code.resume-code")).to_have_text(RESUME_CODE)
            code = b_row.locator("code.resume-code").inner_text()
            run.shot(teacher, "desktop-admin-attempts.png")

        with step("10a the attempts table fits the 1100 px column at 1280 and 1100 px: Delete is not clipped"):
            for w in (1280, 1100):
                teacher.set_viewport_size({"width": w, "height": 800})
                teacher.reload()
                attempts_table_fits(teacher)
            teacher.set_viewport_size({"width": 1280, "height": 800})

        b2 = new_context(**DESKTOP).new_page()
        with step("11 student B resumes on a new desktop device with the code"):
            resume(b2, base_url, code.replace("-", "").lower())
            expect(b2.locator("main#exam")).to_be_visible()
            if page_state(b2) != b_state:
                raise AssertionError(f"resumed page differs: {b_state} → {page_state(b2)}")
            run.shot(b2, "desktop-exam.png")

        with step("12 the old device B1 is signed out of the attempt"):
            b1.reload()
            expect(b1).to_have_url(base_url + "/")
            expect(b1.locator("main#exam")).to_have_count(0)
            question_id, option_id = b_state[0]
            response = b1_context.request.post(
                base_url + "/exam/answer", form={"question": question_id, "option": option_id}
            )
            if response.status != 401:
                raise AssertionError(f"answer from the old device: want 401, got {response.status} {response.text()}")

        with step("13 the used code is refused and B has a new code"):
            fresh = new_context(**phone).new_page()
            resume(fresh, base_url, code)
            expect(fresh.locator("[role=alert]")).to_have_text("That code is not valid.")
            teacher.reload()
            new_code = attempt_row(teacher, BUDI).locator("code.resume-code")
            expect(new_code).to_have_text(RESUME_CODE)
            expect(new_code).not_to_have_text(code)

        with step("14 student B submits on B2 with 4 unanswered and scores 1 / 6"):
            submit(b2, re.compile(r"4 questions have no answer\."))
            b2.locator("#submit-confirm").click()
            expect(b2.locator('main#result[data-show="review"]')).to_be_visible()
            expect(b2.locator("#score")).to_have_text("1 / 6")
            run.shot(b2, "desktop-result-review.png")

        with step("15 teacher sorts attempts by score and by name"):
            teacher.goto(attempts_url)
            teacher.locator("#attempts thead a.sort-link", has_text="Score").click()
            expect(teacher).to_have_url(re.compile(r"sort=score"))
            rows = teacher.locator("#attempts tbody tr[data-attempt-id]")
            expect(rows.first).to_contain_text(AYU)
            expect(teacher.locator("#attempts thead th[aria-sort]")).to_contain_text("Score")
            teacher.locator("#attempts thead a.sort-link", has_text="Name").click()
            expect(teacher).to_have_url(re.compile(r"sort=name"))
            expect(rows.nth(0)).to_contain_text(AYU)
            expect(rows.nth(1)).to_contain_text(BUDI)
            expect(teacher.locator("#attempts thead th[aria-sort]")).to_contain_text("Name")

        with step("15a teacher filters by name; the filter survives sorting and clears"):
            teacher.goto(attempts_url)
            teacher.locator("#name-filter input[name=name]").fill("budi")
            teacher.locator("#name-filter button[type=submit]").click()
            expect(teacher).to_have_url(re.compile(r"[?&]name=budi"))
            rows = teacher.locator("#attempts tbody tr[data-attempt-id]")
            expect(rows).to_have_count(1)
            expect(rows.first).to_contain_text(BUDI)
            expect(teacher.locator(".counts")).to_contain_text("1 of 2 attempts")
            teacher.locator("#attempts thead a.sort-link", has_text="Score").click()
            expect(teacher).to_have_url(re.compile(r"sort=score.*name=budi"))
            expect(rows).to_have_count(1)
            teacher.locator("#filter-clear").click()
            expect(rows).to_have_count(2)

        with step("16 teacher views A's attempt in file order"):
            attempt_row(teacher, AYU).get_by_role("link", name="View").click()
            expect(teacher.locator("main.attempt-detail")).to_be_visible()
            bodies = teacher.locator("section.detail-question .question-body").all_inner_texts()
            if len(bodies) != 6 or not all(q in b for q, b in zip(key, bodies)):
                raise AssertionError(f"questions not in file order: {bodies}")
            expect(teacher.locator(".correct-mark").filter(has_text="✓ Correct")).to_have_count(6)
            expect(teacher.locator(".pick-mark").filter(has_text="Student's pick")).to_have_count(6)
            run.shot(teacher, "desktop-admin-attempt.png")

        with step("16a teacher renames A on the attempt page and back"):
            form = teacher.locator("#rename-form")
            expect(form.locator("input[name=name]")).to_have_value(AYU)
            form.locator("input[name=name]").fill("Ayu L. (5B)")
            form.locator("button[type=submit]").click()
            expect(teacher.locator("main.attempt-detail h1")).to_have_text("Ayu L. (5B)")
            form.locator("input[name=name]").fill(AYU)
            form.locator("button[type=submit]").click()
            expect(teacher.locator("main.attempt-detail h1")).to_have_text(AYU)

        with step("17 teacher exports the CSV"):
            teacher.goto(attempts_url)
            table = download_csv(teacher)
            if table[0] != ["name", "status", "score", "total", "started", "finished", "flags"]:
                raise AssertionError(f"CSV header: {table[0]}")
            if len(table) != 3:
                raise AssertionError(f"CSV rows: want 2, got {table[1:]}")
            a_rows = [r for r in table[1:] if r[0] == AYU]
            if len(a_rows) != 1 or a_rows[0][1:4] != ["submitted", "4", "6"]:
                raise AssertionError(f"CSV row for A: {a_rows}")

        with step("18 teacher deletes B's attempt"):
            attempt_row(teacher, BUDI).get_by_role("link", name="Delete").click()
            expect(teacher.locator(".delete-question")).to_contain_text(BUDI)
            run.shot(teacher, "desktop-admin-delete.png")
            teacher.get_by_role("button", name="Delete attempt").click()
            expect(teacher).to_have_url(re.compile(rf"/admin/exams/{exam_id}/attempts$"))
            expect(teacher.locator("#attempts tbody tr[data-attempt-id]")).to_have_count(1)
            table = download_csv(teacher)
            if len(table) != 2 or table[1][0] != AYU:
                raise AssertionError(f"CSV after delete: {table}")

        with step("19 teacher pages fit a phone without horizontal page overflow"):
            teacher_phone = new_context(**phone).new_page()
            teacher_login(teacher_phone, base_url, password)
            expect(teacher_phone.locator("#exams")).to_be_visible()
            no_page_overflow(teacher_phone)
            run.shot(teacher_phone, "phone-admin-exams.png")
            teacher_phone.goto(attempts_url)
            expect(teacher_phone.locator("#attempts")).to_be_visible()
            no_page_overflow(teacher_phone)
            run.shot(teacher_phone, "phone-admin-attempts.png")
            attempt_row(teacher_phone, AYU).get_by_role("link", name="View").click()
            expect(teacher_phone.locator("main.attempt-detail")).to_be_visible()
            no_page_overflow(teacher_phone)
            run.shot(teacher_phone, "phone-admin-attempt.png")

        algebra_dir = Path(tmp) / "algebra"
        algebra_dir.mkdir()
        algebra_zip, algebra_key = build_fixture_zip(algebra_dir, title="Algebra check", show_result="score")
        with step("20 teacher imports and opens Algebra check; Geometry check closes"):
            teacher.goto(base_url + "/admin/exams")
            import_and_open(teacher, algebra_zip)
            expect(teacher.locator(f"#exams tr[data-exam-id='{exam_id}']")).to_contain_text("Closed")

        with step("21 student C takes Algebra check and sees only the score"):
            c = new_context(**phone).new_page()
            c.goto(base_url + "/")
            expect(c.locator("h1")).to_have_text("Algebra check")
            start(c, base_url, "Citra Dewi")
            answer(c, algebra_key, [True])
            submit(c, re.compile(r"You answered 1 of 6 questions\."))
            c.locator("#submit-confirm").click()
            expect(c.locator('main#result[data-show="score"]')).to_be_visible()
            expect(c.locator("#score")).to_have_text("1 / 6")
            expect(c.locator(".review-question")).to_have_count(0)
            expect(c.locator("[data-correct]")).to_have_count(0)
            run.shot(c, "phone-result-score.png")

        with step("22 health check"):
            response = teacher.context.request.get(base_url + "/healthz")
            if response.status != 200 or response.text().strip() != "ok":
                raise AssertionError(f"/healthz: {response.status} {response.text()!r}")

        browser.close()

    with step("23 no console errors, page errors or external requests"):
        report = run.report()
        problems = [f"console error: {e}" for e in report["console_errors"]]
        problems += [f"page error: {e}" for e in report["page_errors"]]
        problems += [f"external request: {u}" for u in report["external_requests"]]
        if report["requests_total"] == 0:
            problems.append("no requests were recorded")
        if sorted(report["expected_status_errors"]) != sorted(run.expected_status_errors()):
            problems.append(f"expected 401/422 messages once each, got {report['expected_status_errors']}")
        if problems:
            raise AssertionError("\n  ".join(["problems found:"] + problems))


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--admin-password", required=True)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    args.out.mkdir(parents=True, exist_ok=True)
    run = Run(args.base_url.rstrip("/"), args.out)
    code = 0
    try:
        flow(run, args.admin_password)
    except StepFailed as err:
        print(err, file=sys.stderr)
        code = 1
    (args.out / "report.json").write_text(json.dumps(run.report(), indent=2) + "\n", encoding="utf-8")
    if code == 0:
        print(f"OK: {len(run.steps)} steps passed; screenshots and report.json in {args.out}")
    return code


if __name__ == "__main__":
    sys.exit(main())
