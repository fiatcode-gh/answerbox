# /// script
# requires-python = ">=3.12"
# dependencies = ["playwright==1.63.0"]
# ///
"""Browser check for teacher accounts in real Chromium: Google sign-in through
the fake OpenID provider, the Teachers page, exam ownership and removal.
Needs a fresh instance.

uv run e2e/teachers.py --base-url URL --admin-email EMAIL --oidc-url URL --out DIR
"""

import argparse
import re
import sys
import tempfile
from contextlib import contextmanager
from pathlib import Path

from playwright.sync_api import Page, expect, sync_playwright

from common import (
    add_sign_in_args,
    build_fixture_zip,
    confirm,
    foreign_requests,
    import_and_open,
    import_exam,
    refused_login,
    require_cross_site,
    teacher_login,
    watch,
)

DESKTOP = {"viewport": {"width": 1280, "height": 800}}
TEACHER_EMAIL = "teacher@example.test"
TEACHER_NAME = "Tia Teacher"
ADMIN_NAME = "Ada Admin"


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


def teacher_row(page: Page, email: str):
    return page.locator("#teachers tbody tr").filter(has_text=email)


def exam_row(page: Page, exam_id: int):
    return page.locator(f"#exams tbody tr[data-exam-id='{exam_id}']")


def run(base_url: str, admin_email: str, oidc_url: str, out: Path) -> None:
    out.mkdir(parents=True, exist_ok=True)
    logs = []
    with sync_playwright() as p, tempfile.TemporaryDirectory() as tmp:
        browser = p.chromium.launch(headless=True)

        def new_context(**kwargs):
            context = browser.new_context(**kwargs)
            logs.append(watch(context, base_url))
            return context

        a = new_context(**DESKTOP).new_page()
        with step("1 admin signs in"):
            a.goto(f"{base_url}/admin/login")
            expect(a.locator("#google-sign-in")).to_be_visible()
            a.screenshot(path=out / "desktop-login.png", animations="disabled")
            teacher_login(a, base_url, admin_email, ADMIN_NAME)
            expect(a.locator("#signed-in-as")).to_have_text(admin_email)
            expect(a.locator("header").get_by_role("link", name="Teachers")).to_have_count(1)
            expect(a.get_by_text("No exams yet.")).to_have_count(1)

        with step("2 admin adds a teacher"):
            a.goto(f"{base_url}/admin/teachers")
            form = a.locator("#add-teacher-form")
            form.locator("input[name=email]").fill("Teacher@Example.Test")
            form.locator("button[type=submit]").click()
            row = teacher_row(a, TEACHER_EMAIL)
            expect(row).to_have_count(1)
            expect(row).to_contain_text("Never")
            admin_row = teacher_row(a, admin_email)
            expect(admin_row.locator(".badge")).to_have_text("Admin")
            expect(admin_row.get_by_role("link", name="Remove")).to_have_count(0)
            a.screenshot(path=out / "desktop-teachers.png", full_page=True, animations="disabled")

        with step("3 admin imports an exam"):
            (Path(tmp) / "admin").mkdir()
            admin_zip, _ = build_fixture_zip(Path(tmp) / "admin", title="Admin exam")
            a.goto(f"{base_url}/admin/exams")
            admin_exam_id = import_exam(a, admin_zip)
            a.goto(f"{base_url}/admin/exams")
            expect(exam_row(a, admin_exam_id)).to_contain_text("Closed")

        b = new_context(**DESKTOP).new_page()
        with step("4 teacher signs in and sees no exams"):
            teacher_login(b, base_url, TEACHER_EMAIL, TEACHER_NAME)
            expect(b.locator("#signed-in-as")).to_have_text(TEACHER_EMAIL)
            expect(b.locator("header").get_by_role("link", name="Teachers")).to_have_count(0)
            expect(b.get_by_text("No exams yet.")).to_have_count(1)
            for path in ("/admin/teachers", f"/admin/exams/{admin_exam_id}"):
                response = b.context.request.get(base_url + path, max_redirects=0)
                if response.status != 404:
                    raise AssertionError(f"{path} answered {response.status} to the teacher, want 404")

        with step("5 teacher imports and opens an exam"):
            (Path(tmp) / "teacher").mkdir()
            teacher_zip, _ = build_fixture_zip(Path(tmp) / "teacher", title="Teacher exam")
            teacher_exam_id = import_and_open(b, teacher_zip)
            expect(b.locator("#exams tbody tr")).to_have_count(1)
            expect(b.locator("#exams thead th", has_text="Owner")).to_have_count(0)

        with step("6 admin sees both exams with owners"):
            a.goto(f"{base_url}/admin/exams")
            expect(a.locator("#exams tbody tr")).to_have_count(2)
            expect(a.locator("#exams thead th", has_text="Owner")).to_have_count(1)
            expect(exam_row(a, admin_exam_id)).to_contain_text(admin_email)
            expect(exam_row(a, teacher_exam_id)).to_contain_text(TEACHER_EMAIL)
            expect(exam_row(a, teacher_exam_id)).to_contain_text("Open")
            a.screenshot(path=out / "desktop-admin-exams-owners.png", full_page=True, animations="disabled")
            a.goto(f"{base_url}/admin/exams/{teacher_exam_id}")
            expect(a.locator("h1")).to_contain_text("Teacher exam")

        with step("7 teachers page shows the sign-in"):
            a.goto(f"{base_url}/admin/teachers")
            row = teacher_row(a, TEACHER_EMAIL)
            expect(row).to_contain_text(TEACHER_NAME)
            expect(row).not_to_contain_text("Never")
            expect(row.locator("td").nth(3)).to_have_text("1")

        with step("8 admin removes the teacher"):
            teacher_row(a, TEACHER_EMAIL).get_by_role("link", name="Remove").click()
            confirm(a, "remove-teacher-form")
            expect(a).to_have_url(re.compile(r"/admin/teachers$"))
            expect(teacher_row(a, TEACHER_EMAIL)).to_have_count(0)

        with step("9 removed teacher is signed out and refused"):
            b.goto(f"{base_url}/admin/exams")
            expect(b).to_have_url(re.compile(r"/admin/login$"))
            refused_login(b, base_url, TEACHER_EMAIL)

        with step("10 the removed teacher's exam stays with the admin"):
            a.goto(f"{base_url}/admin/exams")
            expect(a.locator("#exams tbody tr")).to_have_count(2)
            expect(exam_row(a, teacher_exam_id)).to_contain_text(TEACHER_EMAIL)

        with step("11 phone width"):
            phone = new_context(**p.devices["Pixel 7"]).new_page()
            teacher_login(phone, base_url, admin_email, ADMIN_NAME)
            expect(phone.locator("#signed-in-as")).to_be_visible()
            no_page_overflow(phone)
            phone.goto(f"{base_url}/admin/teachers")
            expect(phone.locator("#teachers")).to_be_visible()
            expect(phone.locator("#signed-in-as")).to_be_visible()
            no_page_overflow(phone)
            phone.screenshot(path=out / "phone-teachers.png", full_page=True, animations="disabled")

        with step("12 no console errors, page errors or foreign requests"):
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
    parser.add_argument("--out", type=Path, default=Path(__file__).parent / "out" / "teachers")
    args = parser.parse_args()
    base_url, oidc_url = args.base_url.rstrip("/"), args.oidc_url.rstrip("/")
    require_cross_site(base_url, oidc_url)
    try:
        run(base_url, args.admin_email, oidc_url, args.out)
    except StepFailed as err:
        print(err, file=sys.stderr)
        return 1
    print(f"OK: teachers check passed; screenshots in {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
