#!/usr/bin/env python3
"""pdf-press — lint the figures, labels, and content of an architecture spec.

MANAGED FILE — scaffolded by the `pdf-press` skill. Run `/pdf-press upgrade` to re-sync.

  .venv/bin/python lint.py            # everything
  .venv/bin/python lint.py --strict   # warnings become errors

This is the consistency engine. A hand-drawn SVG set only reads as one system if
something mechanical enforces the conventions, and a specification is only
contractual if every normative sentence declares whether the system actually
does it yet. Both are checked here, from .pdf-press.config.json — never from taste.

Exit codes: 0 clean · 1 errors (or warnings under --strict) · 2 config problem
"""

from __future__ import annotations

import argparse
import html as html_mod
import json
import re
import sys
from html.parser import HTMLParser
from pathlib import Path

PIPELINE_VERSION = 3

HEX = re.compile(r"#[0-9a-fA-F]{3,8}\b")
FONT_SIZE_ATTR = re.compile(r'font-size\s*=\s*"([\d.]+)"')
FONT_SIZE_CSS = re.compile(r"font-size\s*:\s*([\d.]+)")
STROKE_WIDTH = re.compile(r'stroke-width\s*=\s*"([\d.]+)"')
RECT = re.compile(r"<rect\b[^>]*>")
ATTR = re.compile(r'([\w:-]+)\s*=\s*"([^"]*)"')
VIEWBOX = re.compile(r'viewBox\s*=\s*"([-\d.\s]+)"')
LABEL_TOKEN = re.compile(r"\{\{t:([\w.\-]+)\}\}")
FIG_TOKEN = re.compile(r"\{\{fig:([\w.\-]+)\}\}")
COMMENT = re.compile(r"<!--.*?-->", re.DOTALL)
ANCHOR_HREF = re.compile(r'href="#([^"]+)"')
EXTERNAL_HREF = re.compile(r'href="(?!#)([^"]*)"')
ELEMENT_ID = re.compile(r'\bid="([^"]+)"')
PRE_BLOCK = re.compile(r"<pre>(.*?)</pre>", re.DOTALL)
LOCAL_HOST = re.compile(r"https://(localhost|127\.|0\.0\.0\.0|10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.)")


def read_content(path: Path) -> str:
    """Read a figure or content file with comments removed, line numbers intact.

    Must stay identical to build.py's strip_comments: if the two disagree on what
    counts as content, lint can pass while the build fails (or the reverse).
    """
    return COMMENT.sub(lambda m: "\n" * m.group(0).count("\n"), path.read_text())


class Report:
    def __init__(self) -> None:
        self.errors: list[str] = []
        self.warnings: list[str] = []

    def error(self, where: str, msg: str) -> None:
        self.errors.append(f"{where}: {msg}")

    def warn(self, where: str, msg: str) -> None:
        self.warnings.append(f"{where}: {msg}")

    def print(self, strict: bool) -> int:
        for w in self.warnings:
            print(f"  warn   {w}")
        for e in self.errors:
            print(f"  ERROR  {e}")
        n_e, n_w = len(self.errors), len(self.warnings)
        if not n_e and not n_w:
            print("pdf-press lint: clean")
            return 0
        print(f"pdf-press lint: {n_e} error(s), {n_w} warning(s)")
        if n_e:
            return 1
        return 1 if strict else 0


def norm_hex(value: str) -> str:
    v = value.lower()
    if len(v) == 4:  # #abc -> #aabbcc
        return "#" + "".join(ch * 2 for ch in v[1:])
    if len(v) == 9:  # #rrggbbaa -> drop alpha
        return v[:7]
    return v


# ──────────────────────────────────────────────── config + assets


def check_config(cfg: dict, root: Path, rep: Report) -> None:
    where = ".pdf-press.config.json"
    langs = cfg["languages"]

    def cover(value, what: str, required: bool = True) -> None:
        if value is None:
            if required:
                rep.error(where, f"{what} is missing")
            return
        if isinstance(value, str):
            return
        missing = [l for l in langs if l not in value]
        if missing:
            rep.error(where, f"{what} has no translation for {', '.join(missing)}")

    cover(cfg["project"].get("title"), "project.title")
    cover(cfg["project"].get("subtitle"), "project.subtitle", required=False)

    if cfg["version"]["policy"] == "date":
        rep.warn(where, "version.policy is \"date\" — builds are not reproducible across months")
    else:
        if not cfg["version"].get("value"):
            rep.error(where, "version.value is required when policy is \"config\"")
        if not cfg["version"].get("date"):
            rep.error(where, "version.date is required when policy is \"config\" (fixes the PDF timestamps)")

    for name, ed in cfg["editions"].items():
        cover(ed.get("label"), f"editions.{name}.label")
        cover(ed.get("marking"), f"editions.{name}.marking")
        cover(ed.get("footerLeft"), f"editions.{name}.footerLeft")
        cover(ed.get("title"), f"editions.{name}.title", required=False)
        cover(ed.get("subtitle"), f"editions.{name}.subtitle", required=False)
        if not ed.get("figures"):
            rep.warn(where, f"editions.{name}.figures is empty — no figure may be embedded")

    theme = cfg["theme"]
    for role, spec in theme["fonts"].items():
        for face in spec.get("faces", []):
            if not (root / face["src"]).exists():
                rep.error(where, f"theme.fonts.{role}: missing font file {face['src']}")
    if theme.get("logo") and not (root / theme["logo"]).exists():
        rep.error(where, f"theme.logo: missing file {theme['logo']}")
    foot = theme.get("footer") or {}
    if foot.get("fontFile") and not (root / foot["fontFile"]).exists():
        rep.error(where, f"theme.footer.fontFile: missing file {foot['fontFile']}")
    if not foot.get("fontFile"):
        rep.warn(where, "theme.footer.fontFile is unset — the stamp falls back to Helvetica, which "
                        "cannot render every Czech diacritic")
    if foot.get("pageWord"):
        cover(foot["pageWord"], "theme.footer.pageWord")
    else:
        non_en = [l for l in langs if l != "en"]
        if non_en:
            rep.warn(where, f"theme.footer.pageWord is unset — the footer will read \"{non_en[0]} 3 / 18\" "
                            f"instead of a real word")
    if not theme.get("signals"):
        rep.warn(where, "theme.signals is empty — figures have no semantic palette to draw from")

    if isinstance(foot.get("color"), list):
        rep.warn(where, "theme.footer.color is a raw [r,g,b] triple — name a theme.colors key "
                        "instead so the stamp colour stays inside the checked palette")
    elif isinstance(foot.get("color"), str) and foot["color"] not in theme["colors"]:
        rep.error(where, f"theme.footer.color: {foot['color']!r} is not a key in theme.colors")

    # Faces that share a (weight, style) slot need unicode-range on all but one, or
    # both rules cover the whole plane, the last one wins, and the missing glyphs
    # survive only on the browser's in-family fallback — which no other renderer
    # guarantees. Faces at different weights/styles never conflict (the standard
    # regular + bold pair is fine).
    for role, spec in theme["fonts"].items():
        faces = spec.get("faces", [])
        if len(faces) > 1:
            slots = {}
            for f in faces:
                slot = (f.get("weight", "400"), f.get("style", "normal"))
                slots.setdefault(slot, []).append(f)
            for slot, group in slots.items():
                without = [f["src"] for f in group if not f.get("unicodeRange")]
                if len(without) > 1:
                    rep.error(where, f"theme.fonts.{role}: {len(without)} faces at weight/style "
                                     f"{'/'.join(slot)} without unicodeRange "
                                     f"({', '.join(Path(s).name for s in without)}) — declare the range "
                                     f"on all but one, or glyph coverage depends on renderer fallback")
        if faces:
            check_glyph_coverage(role, spec, root, langs, cfg, rep)

    check_footer_font(cfg, root, rep)


def _text_for_langs(cfg: dict, langs: list[str]) -> dict[str, str]:
    """Every string the pipeline itself renders, per language — cover, tags, footers."""
    out: dict[str, str] = {l: "" for l in langs}
    proj = cfg["project"]
    for src in (proj.get("title"), proj.get("subtitle")):
        if isinstance(src, dict):
            for l in langs:
                out[l] += src.get(l, "")
    for ed in cfg["editions"].values():
        for key in ("label", "marking", "footerLeft"):
            src = ed.get(key)
            if isinstance(src, dict):
                for l in langs:
                    out[l] += src.get(l, "")
    pw = (cfg["theme"].get("footer") or {}).get("pageWord") or {}
    for l in langs:
        out[l] += pw.get(l, "")
    return out


def _cmap(path: Path) -> set[int] | None:
    try:
        from fontTools.ttLib import TTFont
    except ImportError:
        return None
    try:
        f = TTFont(str(path), fontNumber=0, lazy=True)
        cps: set[int] = set()
        for table in f["cmap"].tables:
            cps |= set(table.cmap.keys())
        f.close()
        return cps
    except Exception:  # noqa: BLE001 — an unreadable font is reported by the caller
        return None


def check_glyph_coverage(role: str, spec: dict, root: Path, langs: list[str], cfg: dict, rep: Report) -> None:
    """The union of a role's faces must cover every character the document renders."""
    union: set[int] = set()
    unreadable = []
    for face in spec.get("faces", []):
        p = root / face["src"]
        if not p.exists():
            continue
        cps = _cmap(p)
        if cps is None:
            unreadable.append(p.name)
        else:
            union |= cps
    if not union:
        if unreadable:
            rep.warn(".pdf-press.config.json", f"theme.fonts.{role}: could not read {', '.join(unreadable)} "
                                       f"(woff2 needs the brotli dep) — glyph coverage unchecked")
        return
    for lang, text in _text_for_langs(cfg, langs).items():
        missing = sorted({ch for ch in text if ch.strip() and ord(ch) not in union})
        if missing:
            rep.error(".pdf-press.config.json", f"theme.fonts.{role}: no glyph for {''.join(missing)!r} "
                                        f"in {lang!r} — a fallback face will draw it")


def check_footer_font(cfg: dict, root: Path, rep: Report) -> None:
    """The stamp is drawn by reportlab, never by the browser — it has no fallback.

    A latin-only TTF here silently draws notdef boxes over every Czech diacritic in
    the page footer of every page. This is the one place a missing glyph is invisible
    until someone reads the printed document.
    """
    foot = (cfg["theme"].get("footer") or {})
    if not foot.get("fontFile"):
        return
    p = root / foot["fontFile"]
    if not p.exists():
        return
    cps = _cmap(p)
    if cps is None:
        rep.warn(".pdf-press.config.json", f"theme.footer.fontFile: could not read {p.name} — "
                                   f"stamp glyph coverage unchecked")
        return
    langs = cfg["languages"]
    for lang in langs:
        text = ""
        for ed in cfg["editions"].values():
            src = ed.get("footerLeft")
            if isinstance(src, dict):
                text += src.get(lang, "")
        pw = (foot.get("pageWord") or {}).get(lang, "")
        text += pw + "0123456789/ ·"
        missing = sorted({ch for ch in text if ch.strip() and ord(ch) not in cps})
        if missing:
            rep.error(".pdf-press.config.json", f"theme.footer.fontFile ({p.name}): no glyph for "
                                        f"{''.join(missing)!r} used in the {lang!r} footer — "
                                        f"reportlab has no fallback, those render as blanks")


def palette(cfg: dict) -> set[str]:
    theme = cfg["theme"]
    allowed = {norm_hex(v) for v in theme["colors"].values()}
    for pair in (theme.get("signals") or {}).values():
        allowed |= {norm_hex(c) for c in pair}
    lint_cfg = (cfg.get("figures") or {}).get("lint") or {}
    allowed |= {norm_hex(c) for c in lint_cfg.get("allowExtraColors", [])}
    allowed |= {"#ffffff", "#000000"}
    return allowed


# ──────────────────────────────────────────────── figures


def check_figures(cfg: dict, root: Path, labels: dict, rep: Report) -> set[str]:
    fig_cfg = cfg.get("figures") or {}
    lint_cfg = fig_cfg.get("lint") or {}
    fdir = root / fig_cfg.get("dir", "figures")
    if not fdir.exists():
        rep.error("figures", f"directory not found: {fdir}")
        return set()

    allowed_colors = palette(cfg)
    allowed_sizes = {float(s) for s in lint_cfg.get("fontSizes", [11, 9.5, 8])}
    want_rx = lint_cfg.get("rx", 4)
    want_stroke = float(lint_cfg.get("strokeWidth", 1.2))
    want_width = cfg["theme"].get("contentWidth", 760)
    enforce_width = lint_cfg.get("enforceViewBoxWidth", True)
    logo = Path(cfg["theme"].get("logo", "")).stem

    # A figure is scaled to fit the text column, so the size on paper is the authored
    # size times that factor — 8 units at viewBox 760 lands at ~5pt on A4, which is
    # below readable print size. Enforcing the authored value alone misses this.
    page = cfg["theme"].get("page") or {}
    page_w_pt = {"A4": 595.276, "LETTER": 612.0}[page.get("size", "A4")]
    margin = (page.get("margin") or "17mm 16mm 21mm 16mm").split()
    side_mm = float(re.sub(r"[a-z]", "", margin[1] if len(margin) > 1 else margin[0]))
    figure_pad_pt = 4 * 72 / 25.4 * 2  # figure padding: 4mm each side, from style.css
    col_pt = page_w_pt - 2 * side_mm * 72 / 25.4 - figure_pad_pt
    fit = col_pt / want_width
    min_rendered = lint_cfg.get("minRenderedPt", 5.0)
    for size in sorted(allowed_sizes):
        if size * fit < min_rendered:
            rep.warn(".pdf-press.config.json",
                     f"figures.lint.fontSizes: {size:g} renders at {size * fit:.2f}pt "
                     f"(fit factor {fit:.3f} from contentWidth {want_width}) — below the "
                     f"{min_rendered:g}pt readable floor; lower contentWidth or raise the size")

    seen: set[str] = set()
    for svg_path in sorted(fdir.glob("*.svg")):
        slug = svg_path.stem
        seen.add(slug)
        if slug == logo:
            continue  # the logo is brand art, not a document figure
        text = read_content(svg_path)
        where = f"{svg_path.name}"

        m = VIEWBOX.search(text)
        if not m:
            rep.error(where, "no viewBox — the figure cannot scale to the content width")
        elif enforce_width:
            parts = m.group(1).split()
            if len(parts) == 4 and abs(float(parts[2]) - want_width) > 0.5:
                rep.error(where, f"viewBox width {parts[2]} != contentWidth {want_width} — "
                                 f"strokes and labels will not match the other figures")

        for raw in set(HEX.findall(text)):
            if norm_hex(raw) not in allowed_colors:
                rep.error(where, f"color {raw} is outside the theme palette — add it to "
                                 f"theme.signals, or to figures.lint.allowExtraColors with a reason")

        sizes = {float(s) for s in FONT_SIZE_ATTR.findall(text)} | {float(s) for s in FONT_SIZE_CSS.findall(text)}
        for bad in sorted(sizes - allowed_sizes):
            rep.error(where, f"font-size {bad:g} is not in figures.lint.fontSizes "
                             f"({', '.join(f'{s:g}' for s in sorted(allowed_sizes))})")

        for sw in sorted({float(s) for s in STROKE_WIDTH.findall(text)}):
            if abs(sw - want_stroke) > 0.001 and sw not in (0.6, 0.8, 1.0):
                rep.warn(where, f"stroke-width {sw:g} — boxes should be {want_stroke:g}")

        for rect in RECT.findall(text):
            attrs = dict(ATTR.findall(rect))
            try:
                w = float(attrs.get("width", "0"))
            except ValueError:
                continue
            if w < 40:
                continue  # decorative swatch or chip, not a box
            try:
                h = float(attrs.get("height", "0"))
                if h and float(attrs.get("rx", "0")) >= h / 2 - 0.5:
                    continue  # fully-rounded pill/badge — a distinct shape, not a box
            except ValueError:
                pass
            if "rx" not in attrs:
                rep.warn(where, f"box rect width={w:g} has no rx — convention is rx={want_rx}")
            elif abs(float(attrs["rx"]) - want_rx) > 0.001:
                rep.error(where, f"box rect rx={attrs['rx']} != {want_rx}")

        for key in set(LABEL_TOKEN.findall(text)):
            entry = labels.get(key)
            if entry is None:
                rep.error(where, f"label key {key!r} is not in labels.json")
            else:
                missing = [l for l in cfg["languages"] if l not in entry]
                if missing:
                    rep.error("labels.json", f"{key!r} has no translation for {', '.join(missing)}")

        check_text_overflow(text, where, cfg, labels, rep)
    return seen


# Mono figure text has a predictable advance (~0.62em for the usual faces), so label
# overflow is estimable without rendering. Heuristic -> warnings, never errors: it
# catches a box title running past its rect and an edge label colliding with a
# neighbouring box — the two defects that otherwise survive until someone reads the
# rendered page (lint sees coordinates, pagecheck sees page blocks, neither sees
# inside a figure).
CHAR_ADVANCE_EM = 0.62
TEXT_EL = re.compile(r"<text\b([^>]*)>(.*?)</text>", re.DOTALL)


def check_text_overflow(svg: str, where: str, cfg: dict, labels: dict, rep: Report) -> None:
    vb = VIEWBOX.search(svg)
    vb_w = float(vb.group(1).split()[2]) if vb and len(vb.group(1).split()) == 4 else None
    rects = []
    for rect in RECT.findall(svg):
        attrs = dict(ATTR.findall(rect))
        try:
            rects.append((float(attrs.get("x", "0")), float(attrs.get("y", "0")),
                          float(attrs["width"]), float(attrs["height"])))
        except (KeyError, ValueError):
            continue
    for m in TEXT_EL.finditer(svg):
        attrs = dict(ATTR.findall("<x " + m.group(1) + ">"))
        try:
            tx, ty = float(attrs.get("x", "0")), float(attrs.get("y", "0"))
            fs = float(attrs.get("font-size", "0"))
        except ValueError:
            continue
        if not fs:
            continue
        raw = m.group(2).strip()
        for lang in cfg["languages"]:
            resolved = LABEL_TOKEN.sub(
                lambda t: (labels.get(t.group(1)) or {}).get(lang, t.group(1)), raw)
            est = len(resolved) * fs * CHAR_ADVANCE_EM
            end = tx + est
            if vb_w and end > vb_w + 1:
                rep.warn(where, f"text {resolved[:40]!r} ({lang}) likely overflows the viewBox "
                                f"(est. end {end:.0f} > {vb_w:g}) — shorten it or move it left")
                continue
            # inside a rect -> must clear that rect's right edge (smallest wins)
            home = [r for r in rects if r[0] <= tx <= r[0] + r[2] and r[1] <= ty <= r[1] + r[3]]
            if home:
                rx, _, rw, _ = min(home, key=lambda r: r[2] * r[3])
                if end > rx + rw - 3:
                    rep.warn(where, f"text {resolved[:40]!r} ({lang}) likely overflows its box "
                                    f"(est. end {end:.0f} > box right {rx + rw:g}) — shorten the label")
                continue
            # outside every rect -> must not run into a neighbouring box's y-band
            for rx, ry, rw, rh in rects:
                if ry <= ty <= ry + rh and tx < rx and end > rx + 2:
                    rep.warn(where, f"text {resolved[:40]!r} ({lang}) likely collides with the box "
                                    f"at x={rx:g} (est. end {end:.0f}) — shorten it or move it")
                    break


# ──────────────────────────────────────────────── content


class ContentLint(HTMLParser):
    """Tracks the open-element stack so a normative sentence can be attributed to
    the nearest enclosing element carrying data-status."""

    def __init__(self, cfg: dict, where: str, rep: Report, markers: list[str]) -> None:
        super().__init__(convert_charrefs=True)
        self.cfg = cfg
        self.where = where
        self.rep = rep
        self.markers = markers
        doctrine = (cfg.get("content") or {}).get("doctrine") or {}
        self.tags_ok = doctrine.get("statusTags", ["IMPLEMENTED", "PLANNED", "PROPOSED"])
        self.require_status = doctrine.get("requireStatusOnNormative", True)
        self.require_source = doctrine.get("requireSourceOnStatus", True)
        self.stack: list[tuple[str, dict]] = []
        self.figure_depth = 0
        self.figure_has_caption = False
        self.figure_line = 0
        self.reported_lines: set[int] = set()

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        a = {k: (v or "") for k, v in attrs}
        if tag not in ("br", "hr", "img", "meta", "link", "use", "path", "circle", "line", "polygon", "stop"):
            self.stack.append((tag, a))
        if "data-status" in a:
            status = a["data-status"]
            if status not in self.tags_ok:
                self.rep.error(f"{self.where}:{self.getpos()[0]}",
                               f"data-status={status!r} is not one of {', '.join(self.tags_ok)}")
            if self.require_source and status == "IMPLEMENTED" and not a.get("data-source"):
                self.rep.error(f"{self.where}:{self.getpos()[0]}",
                               "data-status=\"IMPLEMENTED\" without data-source — an as-built claim "
                               "needs a repo path as its receipt")
        if tag == "figure":
            self.figure_depth += 1
            self.figure_has_caption = False
            self.figure_line = self.getpos()[0]
        if tag == "figcaption" and self.figure_depth:
            self.figure_has_caption = True

    def handle_endtag(self, tag: str) -> None:
        if tag == "figure":
            if self.figure_depth and not self.figure_has_caption:
                self.rep.warn(f"{self.where}:{self.figure_line}", "<figure> without <figcaption>")
            self.figure_depth = max(0, self.figure_depth - 1)
        for i in range(len(self.stack) - 1, -1, -1):
            if self.stack[i][0] == tag:
                del self.stack[i:]
                break

    def handle_data(self, data: str) -> None:
        if not self.require_status or self.figure_depth:
            return
        low = data.lower()
        hit = next((m for m in self.markers if re.search(rf"(?<!\w){re.escape(m)}(?!\w)", low)), None)
        if not hit:
            return
        if any("data-status" in a for _, a in self.stack):
            return
        line = self.getpos()[0]
        if line in self.reported_lines:
            return
        self.reported_lines.add(line)
        self.rep.error(f"{self.where}:{line}",
                       f"normative wording ({hit!r}) in a block with no data-status — tag it "
                       f"IMPLEMENTED (+ data-source) or PLANNED, or soften the sentence")



CODEISH = re.compile(r"<(pre|code)\b.*?</\1>", re.DOTALL | re.IGNORECASE)


def check_glossary(cfg: dict, text: str, lang: str, where: str, rep: Report) -> None:
    """Foreign technical terms whose treatment must be consistent.

    A specification in an inflected language drifts into a pidgin one term at a
    time — `hard-check`, `payloady`, `best-effort` — and each one alone looks
    harmless. The glossary is the project's decision about which loanwords are
    accepted, and this check is what keeps that decision from eroding.

    Prose only: identifiers inside <code>/<pre> ARE the foreign vocabulary and
    must never be "translated".
    """
    glossary = (cfg.get("content") or {}).get("glossary") or []
    if not glossary:
        return
    prose = CODEISH.sub(" ", text)
    for entry in glossary:
        term = entry.get("term", "")
        if not term or entry.get("allow"):
            continue
        scope = entry.get("langs")
        if scope and lang not in scope:
            continue  # a Czech terminology policy must not police an English edition
        hits = len(re.findall(rf"(?<!\w){re.escape(term)}\w*", prose, re.IGNORECASE))
        if not hits:
            continue
        fix = f" — write {entry['prefer']!r}" if entry.get("prefer") else ""
        note = f" ({entry['note']})" if entry.get("note") else ""
        rep.warn(where, f"glossary: {term!r} used {hits}x in prose{fix}{note}")


def check_links(text: str, where: str, rep: Report) -> None:
    """Links a reader will click, checked against what this edition actually ships.

    Two failure modes, both invisible until someone clicks in the finished PDF:
    a jump link whose target sits in a section the edition drops (internal-only
    material is stripped AFTER the link was written), and an external URL that
    was copied out of prose — ellipsised, relative, or pointing at a dev host.
    Reachability of a live URL is NOT checked here: lint must stay offline and
    deterministic. Verify targets live before issuing (skill § Links).
    """
    ids = set(ELEMENT_ID.findall(text))
    for anchor in sorted(set(ANCHOR_HREF.findall(text))):
        if anchor not in ids:
            rep.error(where, f'internal link "#{anchor}" has no target id in this edition — '
                             f"dead jump link (is the target inside a section this edition drops?)")
    for url in sorted(set(EXTERNAL_HREF.findall(text))):
        if not url:
            continue
        if url.startswith(("mailto:", "tel:")):
            continue  # legitimate PDF link targets that are not web URLs
        if url.startswith("http://"):
            rep.error(where, f"external link is plain http: {url}")
        elif not url.startswith("https://"):
            rep.error(where, f"external link is not an absolute https URL: {url} — "
                             f"a relative href cannot resolve from a PDF")
        elif any(ch in url for ch in " …"):
            rep.error(where, f"external link contains a space or an ellipsis: {url} — "
                             f"copied from prose rather than from the target")
        elif LOCAL_HOST.match(url):
            rep.error(where, f"external link points at a local/private address: {url}")


def check_json_blocks(text: str, where: str, rep: Report) -> None:
    """Payload examples belong in canonical 2-space JSON.

    A partner pastes these into a client. Hand-compacted JSON (several keys per
    line, wrapped mid-string by the column width) is what makes a spec look
    hand-maintained — and it is the one formatting defect that can be fixed
    mechanically, so it should never survive review. Blocks that are not JSON
    (state diagrams, side-by-side request/response pairs) are skipped.
    """
    for raw in PRE_BLOCK.findall(text):
        body = html_mod.unescape(raw)
        start = body.find("{")
        if start < 0:
            continue
        candidate = body[start:].strip()
        try:
            obj = json.loads(candidate)
        except (ValueError, RecursionError):
            continue  # not JSON, or a deliberate fragment — not this check's business
        if candidate != json.dumps(obj, ensure_ascii=False, indent=2):
            preview = candidate[:48].replace("\n", " ")
            rep.warn(where, f"JSON example is not in canonical 2-space form ({preview}…) — "
                            f"reformat via json.dumps(obj, ensure_ascii=False, indent=2)")


def check_content(cfg: dict, root: Path, labels: dict, rep: Report, fig_files: set[str]) -> None:
    content_dir = root / (cfg.get("content") or {}).get("dir", "build/content")
    if not content_dir.exists():
        rep.error("content", f"directory not found: {content_dir}")
        return
    doctrine = (cfg.get("content") or {}).get("doctrine") or {}
    markers_by_lang = doctrine.get("normativeMarkers") or {}

    used_figures: set[str] = set()
    used_by_edition: dict[str, set[str]] = {e: set() for e in cfg["editions"]}
    found_any = False
    for edition, ed in cfg["editions"].items():
        derive = ed.get("content")
        for lang in cfg["languages"]:
            src = content_dir / f"{(derive['from'] if derive else edition)}.{lang}.html"
            if not src.exists():
                if not derive:
                    rep.warn("content", f"no {src.name} yet — {edition}.{lang} cannot be built")
                continue
            found_any = True
            text = read_content(src)
            where = src.name
            if derive:
                # Mirror build.py's derivation so the figure scope gate judges
                # what this edition actually ships; prose checks ran on the master.
                for cls in derive.get("dropClasses", []):
                    text = re.sub(
                        rf'<section\b[^>]*class="[^"]*\b{re.escape(cls)}\b[^"]*"[^>]*>.*?</section>\s*',
                        "", text, flags=re.DOTALL)
                where = f"{src.name} (derived: {edition})"
                figs = set(FIG_TOKEN.findall(text))
                used_by_edition[edition] |= figs
                for slug in sorted(figs - set(ed["figures"])):
                    rep.error(where, f"figure {slug!r} is not in editions.{edition}.figures — "
                                     f"scope gate: an out-of-scope figure must be added deliberately")
                # Links are checked on the DERIVED text too: dropping an
                # internal-only section can orphan a jump link that resolves
                # perfectly well in the master.
                check_links(text, where, rep)
                continue

            figs = set(FIG_TOKEN.findall(text))
            used_figures |= figs
            used_by_edition[edition] |= figs
            for slug in sorted(figs - set(ed["figures"])):
                rep.error(where, f"figure {slug!r} is not in editions.{edition}.figures — "
                                 f"scope gate: an out-of-scope figure must be added deliberately")
            for slug in sorted(figs - fig_files):
                rep.error(where, f"figure {slug!r} has no SVG file")

            for key in set(LABEL_TOKEN.findall(text)):
                if key not in labels:
                    rep.error(where, f"label key {key!r} is not in labels.json")

            check_glossary(cfg, text, lang, where, rep)
            check_links(text, where, rep)
            check_json_blocks(text, where, rep)

            markers = [m.lower() for m in markers_by_lang.get(lang, [])]
            if markers:
                ContentLint(cfg, where, rep, markers).feed(text)
            else:
                rep.warn(where, f"no content.doctrine.normativeMarkers for {lang!r} — "
                                f"normative sentences are not being checked")

    if found_any:
        logo = Path(cfg["theme"].get("logo", "")).stem  # brand art, not a document figure
        for slug in sorted(fig_files - used_figures - {logo}):
            rep.warn("figures", f"{slug}.svg is not referenced by any edition")
        # Per edition, not unioned: a union hides an edition that declares a figure
        # its own content never uses — the scope list then lies about what it ships.
        for edition, ed in cfg["editions"].items():
            for slug in sorted(set(ed["figures"]) - used_by_edition[edition]):
                rep.warn(".pdf-press.config.json", f"editions.{edition}.figures lists {slug!r} but no "
                                           f"{edition} content uses it")

    orphans = sorted(set(labels) - _all_label_keys(root, cfg))
    if orphans:
        rep.warn("labels.json", f"{len(orphans)} unused key(s): {', '.join(orphans[:8])}"
                                + (" …" if len(orphans) > 8 else ""))


def _all_label_keys(root: Path, cfg: dict) -> set[str]:
    keys: set[str] = set()
    fdir = root / (cfg.get("figures") or {}).get("dir", "figures")
    if fdir.exists():
        for p in fdir.glob("*.svg"):
            keys |= set(LABEL_TOKEN.findall(read_content(p)))
    cdir = root / (cfg.get("content") or {}).get("dir", "build/content")
    if cdir.exists():
        for p in cdir.glob("*.html"):
            keys |= set(LABEL_TOKEN.findall(read_content(p)))
    return keys


def check_managed_stylesheet(path: Path, rep: Report) -> None:
    """The structural stylesheet claims to hold no brand values. Hold it to that.

    Every literal size or colour that creeps in here is a value no project can retune
    from its config — the claim in the file header quietly becomes false.
    """
    if not path.exists():
        rep.error("build/style.css", "missing — the managed stylesheet was not scaffolded")
        return
    text = COMMENT.sub("", path.read_text())
    text = re.sub(r"/\*.*?\*/", "", text, flags=re.DOTALL)
    sizes = sorted(set(re.findall(r"(?<![\w-])(\d+(?:\.\d+)?)pt", text)))
    hexes = sorted(set(re.findall(r"#[0-9a-fA-F]{3,8}\b", text)))
    if sizes:
        rep.error("build/style.css", f"literal pt size(s) {', '.join(sizes)} — move them into "
                                     f"DEFAULT_SCALE and reference var(--size-*)")
    if hexes:
        rep.error("build/style.css", f"literal colour(s) {', '.join(hexes)} — move them into "
                                     f"theme.colors and reference var(--token)")


def main() -> int:
    ap = argparse.ArgumentParser(prog="lint.py", description="Lint an pdf-press document set.")
    ap.add_argument("--config", default=None)
    ap.add_argument("--strict", action="store_true", help="exit non-zero on warnings too")
    args = ap.parse_args()

    build_dir = Path(__file__).resolve().parent
    cfg_path = Path(args.config).resolve() if args.config else (build_dir.parent / ".pdf-press.config.json")
    if not cfg_path.exists():
        print(f"pdf-press: no config at {cfg_path} — run `/pdf-press setup`")
        return 2
    try:
        cfg = json.loads(cfg_path.read_text())
    except json.JSONDecodeError as e:
        print(f"pdf-press: {cfg_path} is not valid JSON: {e}")
        return 2
    root = cfg_path.parent

    rep = Report()
    check_managed_stylesheet(build_dir / "style.css", rep)
    check_config(cfg, root, rep)
    labels_rel = (cfg.get("figures") or {}).get("labels", "figures/labels.json")
    labels_path = root / labels_rel
    labels: dict = {}
    if labels_path.exists():
        try:
            labels = json.loads(labels_path.read_text())
        except json.JSONDecodeError as e:
            rep.error(labels_rel, f"not valid JSON: {e}")
    fig_files = check_figures(cfg, root, labels, rep)
    check_content(cfg, root, labels, rep, fig_files)
    return rep.print(args.strict)


if __name__ == "__main__":
    sys.exit(main())
