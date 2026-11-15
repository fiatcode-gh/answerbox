"""Shared helpers for the go-exam browser checks (Playwright, sync API)."""

import re
import struct
import zipfile
import zlib
from pathlib import Path

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


def build_fixture_zip(dir, title="Geometry check", show_result="review", monitor=None):
    """Write a 6-question exam zip (exam.md + triangle.png) into dir.

    Returns (zip path, {question text: correct option text}).
    """
    lines = [f"# {title}", "shuffle: questions, options", f"show_result: {show_result}"]
    if monitor is not None:
        lines.append(f"monitor: {monitor}")
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
        z.writestr("triangle.png", _png(160, 120))
    return path, key


def teacher_login(page: Page, base_url: str, password: str) -> None:
    page.goto(f"{base_url}/admin/login")
    form = page.locator("#login-form")
    form.locator("input[name=password]").fill(password)
    form.locator("button[type=submit]").click()
    expect(page).to_have_url(re.compile(r"/admin/exams$"))


def import_exam(page: Page, zip_path) -> int:
    """Import the exam file from the exams page; returns its id (on its detail page)."""
    form = page.locator("#import-form")
    form.locator("input[type=file][name=file]").set_input_files(str(zip_path))
    form.locator("button[type=submit]").click()
    expect(page).to_have_url(re.compile(r"/admin/exams/\d+$"))
    return int(re.search(r"/admin/exams/(\d+)$", page.url).group(1))


def open_exam(page: Page, exam_id: int) -> None:
    """Press Open for exam_id on its detail or the exams page; lands on the exams list."""
    page.locator(f"form[action='/admin/exams/{exam_id}/open'] button[type=submit]").click()
    expect(page).to_have_url(re.compile(r"/admin/exams$"))
    expect(page.locator(f"#exams tr[data-exam-id='{exam_id}']")).to_contain_text("Open")


def import_and_open(page: Page, zip_path) -> int:
    """Import the exam file from the exams page and open it; returns its id."""
    exam_id = import_exam(page, zip_path)
    open_exam(page, exam_id)
    return exam_id


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
