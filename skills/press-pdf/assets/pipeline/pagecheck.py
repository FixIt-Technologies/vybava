#!/usr/bin/env python3
"""pdf-press — audit the PAGINATION of rendered PDFs.

MANAGED FILE — scaffolded by the `pdf-press` skill. Edit the project's
.pdf-press.config.json instead; run `/pdf-press upgrade` to re-sync this file.

  .venv/bin/python pagecheck.py                 # every built target
  .venv/bin/python pagecheck.py external.cs     # one target
  .venv/bin/python pagecheck.py --strict        # layout warnings become errors
  .venv/bin/python pagecheck.py -v              # list every page's fill ratio

`lint.py` checks inputs; `build.py --repro` checks determinism. Neither can see
how the text actually fell onto paper: a heading stranded at the foot of a page,
a chapter tail marooned on a 5%-full page, a caption divorced from its figure,
a line dipping into the footer band. Those are only visible in the OUTPUT, and
they are exactly what makes a specification look unfinished to the person
signing it.

This reads the rendered PDF back with pdfminer.six (real line boxes, real font
sizes), classifies each line against the config's type scale, and reports what
a typographer would circle in red.

Findings are ERRORS when they are unambiguous defects (text outside the text
column, text colliding with the footer stamp) and WARNINGS when they are
judgement calls (a sparse page may be a deliberate section end). `--strict`
makes everything fatal, which is what CI should run.
"""

from __future__ import annotations

import argparse
import statistics
import sys
from pathlib import Path

# Reuse build.py's config loading and page geometry so the two can never
# disagree about where the text column is.
from build import (  # noqa: E402
    DEFAULT_SCALE,
    MM,
    PAGE_SIZES,
    BuildError,
    all_targets,
    load_config,
    out_path,
    resolve_version,
)

try:
    from pdfminer.high_level import extract_pages
    from pdfminer.layout import (
        LAParams, LTCurve, LTImage, LTLine, LTRect, LTTextLineHorizontal,
    )
except ImportError:  # noqa: BLE001 — a missing optional dep must not read as a layout defect
    raise SystemExit(
        "pdf-press: pagecheck needs pdfminer.six —\n"
        "  .venv/bin/pip install -r requirements.txt"
    )

# Defaults, overridable per project under content.pagination.
DEFAULTS = {
    "minFillRatio": 0.25,       # a page emptier than this is suspicious
    "minLastPageFill": 0.08,    # the final page may be short, but not a stray line
    "maxLinesAfterHeading": 1,  # a heading with ≤ this many lines under it is stranded
    "marginTolerancePt": 1.5,   # printers and rounding: don't cry over hairlines
    "widowGapFactor": 1.8,      # gap/median-leading above which a top line reads as a tail
}


class Findings:
    def __init__(self) -> None:
        self.rows: list[tuple[str, str, str]] = []  # (severity, where, message)

    def error(self, where: str, msg: str) -> None:
        self.rows.append(("ERROR", where, msg))

    def warn(self, where: str, msg: str) -> None:
        self.rows.append(("warn ", where, msg))

    def report(self, strict: bool) -> int:
        for sev, where, msg in self.rows:
            print(f"  {'ERROR' if strict else sev}  {where}: {msg}")
        errors = sum(1 for s, _, _ in self.rows if s == "ERROR")
        warns = len(self.rows) - errors
        print(f"pdf-press pagecheck: {errors} error(s), {warns} warning(s)")
        return 1 if errors or (strict and warns) else 0


def content_box(cfg: dict) -> tuple[float, float, float, float, float, float]:
    """(left, right, bottom, top, page_w, page_h) in points."""
    page = cfg["theme"].get("page") or {}
    w, h = PAGE_SIZES[page.get("size", "A4")]
    parts = (page.get("margin") or "17mm 16mm 21mm 16mm").split()
    while len(parts) < 4:  # CSS shorthand: 1, 2 or 3 values
        parts.append(parts[len(parts) % max(1, len(parts) - 1)] if parts else "16mm")
    top, right, bottom, left = (float(p.replace("mm", "")) for p in parts[:4])
    return left * MM, w - right * MM, bottom * MM, h - top * MM, w, h


def role_of(size: float, scale: dict) -> str:
    """Nearest type-scale role for a rendered font size, or '' when off-scale.

    The tolerance is deliberately tight. A real type scale packs several roles
    into a fraction of a point (kicker 7.6 / endmark 7.5 / figcaption 7.4 /
    tableHead 7.2); a loose match makes every kicker look like a table header
    and floods the report with confident nonsense.
    """
    best, best_d = "", 0.12
    for name, want in scale.items():
        d = abs(size - want)
        if d < best_d:
            best, best_d = name, d
    return best


HEADING_ROLES = {"h2", "h3", "h4"}


def is_heading(size: float, scale: dict) -> bool:
    """Bigger than body text and at least h4, and not a role that merely happens
    to be large (a lead paragraph, a contents entry). Size alone is not enough:
    `toc` at 10.5 and `lead` at 10.8 both clear an h4 of 9.8."""
    if not (size >= scale["h4"] - 0.12 and size >= scale["body"] + 0.25):
        return False
    role = role_of(size, scale)
    return role in HEADING_ROLES or role == ""


def looks_like_table_header(line: dict, scale: dict) -> bool:
    """Font size alone cannot identify a table header: a real type scale puts
    `tableHead` within a fraction of a point of several other roles, so a stray
    contents letter or a lowercase table cell matches it. The stylesheet
    uppercases `th`, so require that too — cheap, and it removes the whole
    false-positive class.
    """
    text = line["text"]
    return (role_of(line["size"], scale) == "tableHead"
            and len(text) >= 3 and text == text.upper() and any(c.isalpha() for c in text))


def ink_span(page, box: tuple[float, float, float, float]) -> tuple[float, float] | None:
    """Vertical extent of everything visible inside the text column.

    Text alone under-measures a figure page (a diagram is vector art with a few
    labels), so graphics count too. Page-wide background rects are ignored —
    Chrome paints them behind everything and they would make every page 100% full.
    """
    left, right, bottom, top = box
    lo, hi = None, None
    box_h = top - bottom
    for el in page:
        if isinstance(el, (LTRect, LTCurve, LTLine, LTImage)):
            if (el.y1 - el.y0) > box_h * 0.98 and (el.x1 - el.x0) > (right - left) * 0.98:
                continue  # full-bleed background
        elif not hasattr(el, "__iter__"):
            continue
        y0, y1 = max(el.y0, bottom), min(el.y1, top)
        if y1 <= y0:
            continue
        lo = y0 if lo is None else min(lo, y0)
        hi = y1 if hi is None else max(hi, y1)
    return (lo, hi) if lo is not None else None


def page_lines(page) -> list[dict]:
    out = []
    for element in page:
        if not hasattr(element, "__iter__"):
            continue
        for line in element:
            if not isinstance(line, LTTextLineHorizontal):
                continue
            text = line.get_text().strip()
            if not text:
                continue
            sizes = [c.size for c in line if hasattr(c, "size")]
            out.append({
                "text": text, "x0": line.x0, "x1": line.x1,
                "y0": line.y0, "y1": line.y1,
                "size": max(sizes) if sizes else 0.0,
            })
    out.sort(key=lambda l: -l["y1"])
    return out


def check_pdf(pdf: Path, cfg: dict, opts: dict, find: Findings, verbose: bool) -> None:
    left, right, bottom, top, _pw, _ph = content_box(cfg)
    scale = {**DEFAULT_SCALE, **(cfg["theme"].get("scale") or {})}
    footer_band = bottom - 2  # the stamp lives below the text column
    name = pdf.name

    pages = list(extract_pages(str(pdf), laparams=LAParams()))
    total = len(pages)
    box_h = top - bottom

    for idx, page in enumerate(pages, start=1):
        if idx == 1:
            continue  # the cover is its own design, deliberately sparse
        lines = page_lines(page)
        body = [l for l in lines if l["y1"] > footer_band]
        where = f"{name} p.{idx}"

        # ── unambiguous defects ─────────────────────────────────────────
        tol = opts["marginTolerancePt"]
        for l in body:
            if l["x0"] < left - tol or l["x1"] > right + tol:
                over = max(left - l["x0"], l["x1"] - right)
                find.error(where, f"text outside the text column by {over:.1f}pt: {l['text'][:52]!r}")
                break
        for l in body:
            if l["y0"] < bottom - tol:
                find.error(where, f"text dips {bottom - l['y0']:.1f}pt into the footer band "
                                  f"(collides with the page stamp): {l['text'][:52]!r}")
                break

        if not body:
            find.warn(where, "page carries no text (figure-only page, or an accidental blank)")
            continue

        # ── fill ratio ──────────────────────────────────────────────────
        extent = ink_span(page, (left, right, bottom, top))
        span = (extent[1] - extent[0]) if extent else (
            max(l["y1"] for l in body) - min(l["y0"] for l in body))
        fill = span / box_h if box_h else 1.0
        if verbose:
            print(f"  ..     {where}: fill {fill * 100:4.1f}%  lines {len(body)}")
        floor = opts["minLastPageFill"] if idx == total else opts["minFillRatio"]
        if fill < floor:
            find.warn(where, f"page is only {fill * 100:.0f}% full ({len(body)} lines) — "
                             f"a chapter tail stranded before a forced break; move or trim "
                             f"content, or let the section start earlier")

        # ── stranded headings / table headers ───────────────────────────
        for i, l in enumerate(body):
            role = role_of(l["size"], scale)
            after = len(body) - i - 1
            if is_heading(l["size"], scale) and after <= opts["maxLinesAfterHeading"]:
                find.warn(where, f"heading {l['text'][:42]!r} sits at the page foot with "
                                 f"{after} line(s) under it — add `page-break-after: avoid` "
                                 f"or reflow the section")
            if after == 0 and looks_like_table_header(l, scale):
                find.warn(where, f"table header row is alone at the page foot — the table's "
                                 f"body starts on the next page")

        # ── widow: a paragraph tail marooned at the top ─────────────────
        if len(body) >= 3:
            gaps = [body[i]["y1"] - body[i + 1]["y1"] for i in range(len(body) - 1)]
            median = statistics.median(gaps)
            # Only body text can be a widow: a kicker, a heading or a caption
            # opening a page is the layout working, not failing.
            if (median > 0 and gaps[0] > median * opts["widowGapFactor"]
                    and role_of(body[0]["size"], scale) == "body"):
                find.warn(where, f"page opens with a lone line before a {gaps[0]:.0f}pt gap — "
                                 f"a paragraph tail carried over: {body[0]['text'][:42]!r}")

        # ── caption divorced from its figure ────────────────────────────
        if role_of(body[0]["size"], scale) == "figcaption":
            find.warn(where, "page opens with a figure caption — the figure it describes is "
                             "on the previous page")


def main() -> int:
    ap = argparse.ArgumentParser(prog="pagecheck.py",
                                 description="Audit pagination of the rendered pdf-press PDFs.")
    ap.add_argument("targets", nargs="*", help="edition.lang pairs; default is every built target")
    ap.add_argument("--config", default=None)
    ap.add_argument("--strict", action="store_true", help="treat layout warnings as errors")
    ap.add_argument("-v", "--verbose", action="store_true", help="print every page's fill ratio")
    args = ap.parse_args()

    build_dir = Path(__file__).resolve().parent
    cfg_path = Path(args.config).resolve() if args.config else (build_dir.parent / ".pdf-press.config.json")
    cfg = load_config(cfg_path)
    root = cfg_path.parent
    version = resolve_version(cfg)[0]
    opts = {**DEFAULTS, **((cfg.get("content") or {}).get("pagination") or {})}

    find = Findings()
    checked = 0
    for t in (args.targets or all_targets(cfg)):
        if "." not in t:
            raise BuildError(f"target {t!r} is not <edition>.<lang>")
        edition, lang = t.split(".", 1)
        pdf = out_path(cfg, root, edition, lang, version)
        if not pdf.exists():
            if args.targets:  # explicitly asked for → say so
                find.error(t, f"no built PDF at {pdf} — run build.py first")
            continue
        checked += 1
        check_pdf(pdf, cfg, opts, find, args.verbose)

    if not checked:
        print("pdf-press pagecheck: no built PDFs found — run build.py first")
        return 1
    return find.report(args.strict)


if __name__ == "__main__":
    sys.exit(main())
