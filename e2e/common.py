"""Shared helpers for the Answerbox browser checks (Playwright, sync API)."""

import re
import struct
import zipfile
import zlib
from pathlib import Path
from urllib.parse import urlsplit

from playwright.sync_api import BrowserContext, Page, expect

# Question text → correct option text, in file order. Option bodies are written
# to the file exactly as listed here, with "*" marking the correct one.
_QUESTIONS = [
    (
        "What is the area of this right triangle with legs 3 and 4?",
        ["6 *", "7", "12", "14"],
        "![triangle](triangle.png)",
    ),
    (
        "What does this program print?",
        ["5 *", "23", "2 + 3", "Nothing"],
        "```\nprint(2 + 3)\n```",
    ),
    ("Which city is the capital of France?", ["Paris *", "**Lyon**", "Nice", "Lille"], None),
    ("How many sides does a hexagon have?", ["6 *", "5", "7", "8"], None),
    ("What is 7 × 8?", ["56 *", "54", "48", "64"], None),
    ("Which planet is closest to the Sun?", ["Mercury *", "Venus", "Earth", "Mars"], None),
]


def _png(width: int, height: int) -> bytes:
    """A two-colour PNG: a blue right triangle on a light background."""
    paper, ink = b"\xfb\xfa\xf7", b"\x1f\x4f\xa3"
    rows = bytearray()
    for y in range(height):
        rows.append(0)  # filter: none
        edge = (y + 1) * width // height
        for x in range(width):
            rows += ink if x < edge else paper

    def chunk(kind: bytes, data: bytes) -> bytes:
        return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data))

    header = struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0)
    return b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", header) + chunk(b"IDAT", zlib.compress(bytes(rows))) + chunk(b"IEND", b"")


def write_png(path, width: int, height: int) -> Path:
    """Write a width × height PNG (the fixture triangle) to path; returns path."""
    path = Path(path)
    path.write_bytes(_png(width, height))
    return path


def build_fixture_zip(dir, title="Geometry check", show_result="review", monitor=None, duration=None, image_size=(160, 120)):
    """Write a 6-question exam zip (exam.md + triangle.png) into dir.

    Returns (zip path, {question text: correct option text}).
    """
    lines = [f"# {title}", "shuffle: questions, options", f"show_result: {show_result}"]
    if monitor is not None:
        lines.append(f"monitor: {monitor}")
    if duration is not None:
        lines.append(f"duration: {duration}")
    lines.append("")
    key = {}
    for n, (text, options, extra) in enumerate(_QUESTIONS, start=1):
        lines.append(f"{n}. {text}")
        if extra:
            lines.append(extra)
        for option in options:
            lines.append(f"- {option}")
            if option.endswith(" *"):
                key[text] = option.removesuffix(" *")
        lines.append("")
    path = Path(dir) / "exam.zip"
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as z:
        z.writestr("exam.md", "\n".join(lines))
        z.writestr("triangle.png", _png(*image_size))
    return path, key


NOT_ALLOWED = "This Google account is not allowed. Ask your Answerbox admin to add it."


def add_sign_in_args(parser) -> None:
    parser.add_argument("--admin-email", required=True, help="an email in the server's ANSWERBOX_ADMIN_EMAILS")
    parser.add_argument("--oidc-url", required=True, help="base URL of the fake OpenID provider the server uses")


def require_cross_site(base_url: str, oidc_url: str) -> None:
    """Exit unless the app and the provider are on different hostnames."""
    if urlsplit(base_url).hostname == urlsplit(oidc_url).hostname:
        raise SystemExit(
            "run the app on localhost and the fake provider on 127.0.0.1: the sign-in"
            " callback must cross sites as it does with Google"
        )


def _sign_in_at_provider(page: Page, base_url: str, email: str, name: str) -> None:
    page.goto(f"{base_url}/admin/login")
    page.locator("#google-sign-in").click()
    form = page.locator("#fake-signin")
    form.locator("input[name=email]").fill(email)
    form.locator("input[name=name]").fill(name)
    form.locator("button[type=submit]").click()


def teacher_login(page: Page, base_url: str, email: str, name: str = "Test Teacher") -> None:
    """Sign in through the fake provider; lands on the exams page."""
    _sign_in_at_provider(page, base_url, email, name)
    expect(page).to_have_url(re.compile(r"/admin/exams$"))
    expect(page.locator("#signed-in-as")).to_have_text(email)


def refused_login(page: Page, base_url: str, email: str) -> None:
    """Sign in through the fake provider as an account the app refuses."""
    _sign_in_at_provider(page, base_url, email, "Refused Teacher")
    expect(page).to_have_url(re.compile(r"/admin/login\?error=not_allowed$"))
    expect(page.locator("[role=alert]")).to_have_text(NOT_ALLOWED)


def foreign_requests(urls, base_url: str, oidc_url: str) -> list[str]:
    """The URLs that start with neither base_url nor oidc_url."""
    return [u for u in urls if not u.startswith(base_url) and not u.startswith(oidc_url)]


def import_exam(page: Page, zip_path) -> int:
    """Import the exam file from the exams page; returns its id (on its detail page)."""
    form = page.locator("#import-form")
    form.locator("input[type=file][name=file]").set_input_files(str(zip_path))
    form.locator("button[type=submit]").click()
    expect(page).to_have_url(re.compile(r"/admin/exams/\d+$"))
    return int(re.search(r"/admin/exams/(\d+)$", page.url).group(1))


def open_exam(page: Page, exam_id: int) -> None:
    """Press Open for exam_id on its exam page or on the exams list; ends on the exams list.

    Pressed on the exam page, Open lands back on it; this then goes to the list.
    """
    on_exam_page = re.search(rf"/admin/exams/{exam_id}$", page.url) is not None
    page.locator(f"form[action='/admin/exams/{exam_id}/open'] button[type=submit]").click()
    if on_exam_page:
        expect(page).to_have_url(re.compile(rf"/admin/exams/{exam_id}$"))
        expect(page.locator(".exam-head .badge")).to_have_text("Open")
        parts = urlsplit(page.url)
        page.goto(f"{parts.scheme}://{parts.netloc}/admin/exams")
    expect(page).to_have_url(re.compile(r"/admin/exams$"))
    expect(page.locator(f"#exams tr[data-exam-id='{exam_id}']")).to_contain_text("Open")


def import_and_open(page: Page, zip_path) -> int:
    """Import the exam file from the exams page and open it; returns its id."""
    exam_id = import_exam(page, zip_path)
    open_exam(page, exam_id)
    return exam_id


EXAM_CODE = re.compile(r"^[ABCDEFGHJKMNPQRSTVWXYZ2-9]{6}$")
REFUSED = "No open exam has that code. Check it with your teacher."


def exam_code(page: Page, exam_id: int) -> str:
    """The code in the exam's row; the page must be on the exams list."""
    code = page.locator(f"#exams tr[data-exam-id='{exam_id}'] td.code").inner_text().strip()
    if not EXAM_CODE.match(code):
        raise AssertionError(f"exam {exam_id} has code {code!r}, want 6 symbols of the code alphabet")
    return code


def start(page: Page, base_url: str, code: str, name: str) -> None:
    """Open the landing page, type the code and the name, press Start; lands on the exam."""
    page.goto(base_url + "/")
    form = page.locator("#start-form")
    form.locator("input[name=code]").fill(code)
    form.locator("input[name=name]").fill(name)
    form.locator("button[type=submit]").click()
    expect(page.locator("main#exam")).to_be_visible()


def watch(context: BrowserContext, base_url: str) -> dict:
    """Record every request URL, console error and page error in context."""
    log = {"requests": [], "console_errors": [], "page_errors": []}

    def on_page(page: Page) -> None:
        page.on(
            "console",
            lambda msg: msg.type == "error" and log["console_errors"].append(f"{page.url}: {msg.text}"),
        )
        page.on("pageerror", lambda err: log["page_errors"].append(f"{page.url}: {err}"))

    context.on("page", on_page)
    context.on("request", lambda req: log["requests"].append(req.url))
    return log
