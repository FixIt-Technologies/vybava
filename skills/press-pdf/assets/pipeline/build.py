#!/usr/bin/env python3
"""pdf-press — build enterprise architecture specification PDFs.

MANAGED FILE — scaffolded by the `pdf-press` skill. Edit the project's
.pdf-press.config.json instead; run `/pdf-press upgrade` to re-sync this file.

  .venv/bin/python build.py                     # every edition x language
  .venv/bin/python build.py internal.cs         # one target
  .venv/bin/python build.py --list              # what would be built
  .venv/bin/python build.py --repro internal.cs # build twice, prove byte-identical

Pipeline per document:
  .pdf-press.config.json  ->  generated theme.css (tokens: fonts, palette, page, cover)
  content/<edition>.<lang>.html
      + {{fig:slug}}   -> figures/<slug>.svg, {{t:key}} labels resolved per language
      + {{version}} {{title}} {{subtitle}} {{label}} {{marking}} {{motif}} {{logo}}
  -> assembled HTML  -> headless Chrome --print-to-pdf
  -> reportlab footer/page-number stamp  -> deterministic metadata rewrite
  -> <output.dir>/<output.pattern>

Determinism: no wall-clock anywhere. Version and PDF dates come from the config;
reportlab runs in invariant mode; the trailer /ID is derived from the document
content. Same inputs + same Chrome build => byte-identical PDF. See
references/determinism.md in the skill.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import shutil
import subprocess
import sys
import tempfile
from io import BytesIO
from pathlib import Path

import typography

PIPELINE_VERSION = 5

CHROME_CANDIDATES = [
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
    "/Applications/Chromium.app/Contents/MacOS/Chromium",
    "google-chrome",
    "chromium",
    "chromium-browser",
]

PAGE_SIZES = {"A4": (595.276, 841.890), "LETTER": (612.0, 792.0)}

# Every size the stylesheet uses lives here, so `style.css` can honestly claim to
# hold no literal values and a project can retune the whole document from config.
DEFAULT_SCALE = {
    "body": 9.4, "lead": 10.8, "small": 8.0, "h2": 17.0, "h3": 11.5, "h4": 9.8,
    "coverTitle": 34.0, "coverSubtitle": 12.0, "coverBrand": 12.0, "coverMeta": 8.0,
    "table": 8.6, "tableHead": 7.2, "pre": 7.8,
    "kicker": 7.6, "figcaption": 7.4, "figNo": 7.4,
    "toc": 10.5, "tocSub": 9.3, "tocNo": 8.5,
    "chapterNo": 10.0, "editionTag": 8.5,
    "callout": 9.0, "calloutTitle": 7.2,
    "chip": 7.4, "endmark": 7.5, "status": 6.4, "statusNote": 7.0,
}

MM = 72 / 25.4


class BuildError(SystemExit):
    def __init__(self, msg: str) -> None:
        super().__init__(f"pdf-press: {msg}")


# ──────────────────────────────────────────────────────────── config


def load_config(path: Path) -> dict:
    if not path.exists():
        raise BuildError(
            f"no config at {path}.\n"
            "  This project has not been set up yet — run `/pdf-press setup`."
        )
    try:
        cfg = json.loads(path.read_text())
    except json.JSONDecodeError as e:
        raise BuildError(f"{path} is not valid JSON: {e}")

    for key in ("schemaVersion", "pipelineVersion", "project", "version", "languages", "editions", "theme"):
        if key not in cfg:
            raise BuildError(f"{path}: missing required key {key!r}")
    if cfg["schemaVersion"] != 1:
        raise BuildError(f"{path}: schemaVersion {cfg['schemaVersion']} — this pipeline speaks 1")
    if cfg["pipelineVersion"] > PIPELINE_VERSION:
        raise BuildError(
            f"{path}: pipelineVersion {cfg['pipelineVersion']} is newer than this build.py "
            f"({PIPELINE_VERSION}) — run `/pdf-press upgrade`"
        )
    return cfg


def resolve_version(cfg: dict) -> tuple[str, str]:
    """Return (version_label, pdf_date_string). Never reads the clock in config mode."""
    v = cfg["version"]
    if v["policy"] == "config":
        if not v.get("value"):
            raise BuildError("version.policy is \"config\" but version.value is missing")
        if not v.get("date"):
            raise BuildError(
                "version.policy is \"config\" but version.date is missing — "
                "without a fixed date the PDF timestamps differ on every build"
            )
        d = v["date"].replace("-", "")
        return v["value"], f"D:{d}000000Z"
    from datetime import date  # only reachable in the explicitly non-reproducible mode
    today = date.today()
    return f"v{today:%Y.%m}", f"D:{today:%Y%m%d}000000Z"


def i18n(value: dict | str | None, lang: str, what: str, required: bool = True) -> str:
    if value is None:
        if required:
            raise BuildError(f"{what}: missing")
        return ""
    if isinstance(value, str):
        return value
    if lang not in value:
        raise BuildError(f"{what}: no {lang!r} translation (has {sorted(value)})")
    return value[lang]


# ──────────────────────────────────────────────────────────── theme -> css


def resolve_color(value, theme: dict) -> tuple[float, float, float]:
    """Accept a palette token name (preferred) or a raw [r,g,b] triple.

    A raw triple in the footer config is a fourth colour source nothing validates —
    naming a token keeps the stamp inside the palette the linter checks.
    """
    if isinstance(value, str):
        hexv = theme["colors"].get(value)
        if not hexv:
            raise BuildError(
                f"theme.footer.color: {value!r} is not a key in theme.colors "
                f"(have {', '.join(sorted(theme['colors']))})"
            )
        return tuple(int(hexv[i:i + 2], 16) / 255 for i in (1, 3, 5))  # type: ignore[return-value]
    if isinstance(value, list) and len(value) == 3:
        return tuple(float(c) for c in value)  # type: ignore[return-value]
    raise BuildError(f"theme.footer.color: expected a theme.colors key or [r,g,b], got {value!r}")


def font_stack(role: dict | None, fallback: str) -> str:
    if not role:
        return fallback
    stack = role.get("stack") or fallback
    return f"'{role['family']}', {stack}"


def gen_theme_css(cfg: dict, root: Path, lang: str | None = None) -> str:
    theme = cfg["theme"]
    out: list[str] = ["/* generated from .pdf-press.config.json — do not edit */"]

    for role in ("body", "display", "mono"):
        spec = theme["fonts"].get(role)
        if not spec:
            continue
        for face in spec.get("faces", []):
            src = (root / face["src"]).resolve()
            if not src.exists():
                raise BuildError(f"theme.fonts.{role}: font file not found: {src}")
            parts = [
                f"  font-family: '{spec['family']}';",
                f"  src: url('{src.as_uri()}') format('{face.get('format', 'woff2')}');",
                f"  font-weight: {face.get('weight', '400')};",
                f"  font-style: {face.get('style', 'normal')};",
            ]
            if face.get("unicodeRange"):
                parts.append(f"  unicode-range: {face['unicodeRange']};")
            out.append("@font-face {\n" + "\n".join(parts) + "\n}")

    vars_: list[str] = []
    for name, value in theme["colors"].items():
        vars_.append(f"  --{name}: {value};")
    for name, (stroke, wash) in (theme.get("signals") or {}).items():
        vars_.append(f"  --{name}: {stroke};")
        vars_.append(f"  --{name}-wash: {wash};")
    vars_.append(f"  --font-body: {font_stack(theme['fonts'].get('body'), 'sans-serif')};")
    vars_.append(f"  --font-display: {font_stack(theme['fonts'].get('display') or theme['fonts'].get('body'), 'sans-serif')};")
    vars_.append(f"  --font-mono: {font_stack(theme['fonts'].get('mono'), 'monospace')};")

    scale = {**DEFAULT_SCALE, **(theme.get("scale") or {})}
    for name, size in scale.items():
        vars_.append(f"  --size-{name}: {size}pt;")

    # Cover height must follow the page, not a constant. A 296 mm cover on LETTER
    # (279.4 mm) spills onto a second page: the meta block lands in the top margin
    # and, because the stamp only skips page 1, every page number shifts.
    page = theme.get("page") or {}
    page_size = page.get("size", "A4")
    page_h_mm = PAGE_SIZES[page_size][1] / MM
    cover = theme.get("cover") or {}
    cover_h = cover.get("heightMm", round(page_h_mm - 1, 1))
    if cover_h >= page_h_mm:
        clamped = round(page_h_mm - 1, 1)
        print(f"pdf-press: warning — theme.cover.heightMm {cover_h} does not fit "
              f"{page_size} ({page_h_mm:.1f}mm); clamping to {clamped}")
        cover_h = clamped
    vars_.append(f"  --cover-height: {cover_h}mm;")
    out.append(":root {\n" + "\n".join(vars_) + "\n}")

    out.append(f"@page {{ size: {page_size}; margin: {page.get('margin', '17mm 16mm 21mm 16mm')}; }}")
    out.append("@page cover { margin: 0; }")

    style = cover.get("style", "panel")
    out.append(f"/* cover style: {style} */")
    if style == "hero-fill":
        # A brand accent is not always the right full-bleed field: a dark cover with
        # a light accent (navy page, teal pills) needs the two decoupled, and its
        # text must flip to stay readable. Both default to the old behaviour.
        cover_bg = cover.get("background") or "var(--brand)"
        cover_ink = cover.get("ink") or "var(--ink)"
        out.append(f".cover {{ background: {cover_bg}; color: {cover_ink}; }}")
        out.append(f".cover h1, .cover .subtitle, .cover .meta, .cover .kicker,"
                   f" .cover .brand, .cover .meta .confidential {{ color: {cover_ink}; }}")
        # Streaks are injected as an inline SVG layer at assembly time (see
        # inject_cover_art) — CSS mask/repeating-gradient streaks force Chrome's
        # print pipeline to rasterize the cover at low DPI (visible pixelation);
        # vector rects with gradient fades stay crisp at any resolution.
    elif style == "panel":
        out.append(".cover { background: var(--paper); color: var(--ink); border-top: 6mm solid var(--brand); }")
    else:
        out.append(".cover { background: var(--paper); color: var(--ink); }")

    # Status badges are printed by CSS from data-status, so their wording is a
    # theme concern, not content. Without this a Czech document stamps every
    # normative sentence with an English word.
    labels = ((cfg.get("content") or {}).get("doctrine") or {}).get("statusLabels") or {}
    if labels and lang:
        for status, per_lang in labels.items():
            word = per_lang.get(lang) if isinstance(per_lang, dict) else per_lang
            if not word:
                continue
            # `html [data-status=…]` outranks style.css's `[data-status]` rule,
            # which is injected after this sheet.
            out.append(f'html [data-status="{status}"]::after {{ content: "{word}"; }}')
            # A single pictographic glyph (emoji) carries its own colour and shape —
            # the word-badge chip (border, wash, tracking) around it is noise.
            if len(word) <= 2 and not any(ch.isalnum() for ch in word):
                out.append(
                    f'html [data-status="{status}"]::after {{ border: 0; background: transparent;'
                    f' padding: 0; letter-spacing: 0; font-size: 0.8em; }}')

    motif = cover.get("motif") or ""
    if motif:
        out.append(f'.kicker::before {{ content: "{motif} "; color: var(--brand-ink, var(--ink)); letter-spacing: 0; }}')
        out.append(f'.edition-tag::before {{ content: "{motif}"; letter-spacing: 0; }}')
    return "\n".join(out) + "\n"


# ──────────────────────────────────────────────────────────── assembly


COMMENT = re.compile(r"<!--.*?-->", re.DOTALL)


def strip_comments(text: str) -> str:
    """Drop HTML/SVG comments before any token work.

    Comments are authoring notes, not content: they never render, and a note that
    documents a token (`{{t:key}}`, `{{fig:slug}}`) must not be mistaken for a real
    reference. lint.py strips identically — the two must agree on what counts as
    content or a clean lint could still fail the build.

    Newlines inside a comment are preserved so reported line numbers stay true to
    the file on disk.
    """
    return COMMENT.sub(lambda m: "\n" * m.group(0).count("\n"), text)


def load_labels(root: Path, cfg: dict) -> dict:
    rel = (cfg.get("figures") or {}).get("labels", "figures/labels.json")
    p = root / rel
    if not p.exists():
        return {}
    try:
        return json.loads(p.read_text())
    except json.JSONDecodeError as e:
        raise BuildError(f"{p}: not valid JSON: {e}")


def resolve_labels(text: str, lang: str, labels: dict, where: str) -> str:
    def sub(m: re.Match) -> str:
        key = m.group(1)
        entry = labels.get(key)
        if entry is None:
            raise BuildError(f"{where}: label key {key!r} is not in labels.json")
        if lang not in entry:
            raise BuildError(f"{where}: label {key!r} has no {lang!r} translation")
        return entry[lang]

    return re.sub(r"\{\{t:([\w.\-]+)\}\}", sub, text)


def inline_svg(slug: str, lang: str, labels: dict, root: Path, cfg: dict) -> str:
    fdir = (cfg.get("figures") or {}).get("dir", "figures")
    path = root / fdir / f"{slug}.svg"
    if not path.exists():
        raise BuildError(f"figure {slug!r}: no such file {path}")
    svg = resolve_labels(strip_comments(path.read_text()), lang, labels, f"figure {slug}")
    return re.sub(r"^<\?xml[^>]*\?>\s*", "", svg).strip()



def inject_cover_art(body: str, cfg: dict) -> str:
    """hero-fill streak layer, injected as inline SVG so it stays vector in print.

    CSS mask/repeating-gradient streaks force Chrome to rasterize the cover at low
    DPI; vector rects with a gradient fade are resolution-independent. Skipped when
    the content ships its own `cover-art` layer, or the cover is not hero-fill."""
    if (cfg["theme"].get("cover") or {}).get("style") != "hero-fill":
        return body
    if "cover-art" in body:
        return body
    m = re.search(r'<div\b[^>]*class="[^"]*\bcover\b[^"]*"[^>]*>', body)
    if not m:
        return body
    art = (
        '<svg class="cover-art" viewBox="0 0 210 296" preserveAspectRatio="none" '
        'xmlns="http://www.w3.org/2000/svg" aria-hidden="true">'
        '<defs><linearGradient id="pp-cover-streak" x1="0" y1="0" x2="0" y2="1">'
        '<stop offset="0" stop-color="#ffffff" stop-opacity="0.34"/>'
        '<stop offset="0.45" stop-color="#ffffff" stop-opacity="0.14"/>'
        '<stop offset="0.75" stop-color="#ffffff" stop-opacity="0"/>'
        '</linearGradient></defs>'
        '<g transform="skewX(-25)" fill="url(#pp-cover-streak)">'
        '<rect x="30" width="6" height="296"/><rect x="64" width="14" height="296"/>'
        '<rect x="110" width="6" height="296"/><rect x="148" width="20" height="296"/>'
        '<rect x="204" width="10" height="296"/><rect x="248" width="26" height="296"/>'
        '<rect x="296" width="14" height="296"/>'
        '</g></svg>'
    )
    return body[:m.end()] + art + body[m.end():]


def assemble(cfg: dict, root: Path, edition: str, lang: str, version: str) -> str:
    if edition not in cfg["editions"]:
        raise BuildError(f"unknown edition {edition!r} (have {sorted(cfg['editions'])})")
    if lang not in cfg["languages"]:
        raise BuildError(f"unknown language {lang!r} (have {cfg['languages']})")

    ed = cfg["editions"][edition]
    content_dir = root / (cfg.get("content") or {}).get("dir", "build/content")
    # A derived edition renders another edition's file (one tagged master,
    # N audience renderings) instead of maintaining a twin that drifts.
    derive = ed.get("content")
    src_edition = derive["from"] if derive else edition
    if derive and derive["from"] not in cfg["editions"]:
        raise BuildError(f"editions.{edition}.content.from: unknown edition {derive['from']!r}")
    src = content_dir / f"{src_edition}.{lang}.html"
    if not src.exists():
        raise BuildError(
            f"no content at {src}\n"
            f"  Author it first, or scaffold from the skill's assets/templates/."
        )

    labels = load_labels(root, cfg)
    body = strip_comments(src.read_text())
    if derive:
        for cls in derive.get("dropClasses", []):
            # Top-level sections only — the schema documents the no-nesting rule.
            body = re.sub(
                rf'<section\b[^>]*class="[^"]*\b{re.escape(cls)}\b[^"]*"[^>]*>.*?</section>\s*',
                "", body, flags=re.DOTALL)
        if derive.get("stripStatus"):
            body = re.sub(r'\s+data-(?:status|source)="[^"]*"', "", body)

    allowed = set(ed["figures"])
    used = set(re.findall(r"\{\{fig:([\w.\-]+)\}\}", body))
    leaked = sorted(used - allowed)
    if leaked:
        raise BuildError(
            f"{src.name}: figures not allowed in the {edition!r} edition: {', '.join(leaked)}\n"
            f"  editions.{edition}.figures is the scope gate — add them deliberately or remove the reference."
        )

    body = re.sub(r"\{\{fig:([\w.\-]+)\}\}", lambda m: inline_svg(m.group(1), lang, labels, root, cfg), body)

    logo_svg = ""
    if cfg["theme"].get("logo"):
        logo_path = root / cfg["theme"]["logo"]
        if not logo_path.exists():
            raise BuildError(f"theme.logo: no such file {logo_path}")
        logo_svg = re.sub(r"^<\?xml[^>]*\?>\s*", "", logo_path.read_text()).strip()

    tokens = {
        "version": version,
        "lang": lang,
        "edition": edition,
        "project": cfg["project"]["name"],
        # An edition may be a different document sharing the pipeline (e.g. a
        # supplier spec) — its title/subtitle override the project's.
        "title": i18n(ed.get("title") or cfg["project"].get("title"), lang,
                      f"editions.{edition}.title" if ed.get("title") else "project.title"),
        "subtitle": i18n((ed.get("subtitle") or cfg["project"].get("subtitle")) if ed.get("title")
                         else cfg["project"].get("subtitle"),
                         lang, "subtitle", required=False),
        "label": i18n(ed.get("label"), lang, f"editions.{edition}.label"),
        "marking": i18n(ed.get("marking"), lang, f"editions.{edition}.marking"),
        "motif": (cfg["theme"].get("cover") or {}).get("motif", ""),
        "logo": logo_svg,
        "editionTagStyle": ed.get("tagStyle", "ink"),
    }
    for name, value in tokens.items():
        body = body.replace(f"{{{{{name}}}}}", value)
    body = resolve_labels(body, lang, labels, src.name)

    leftover = re.findall(r"\{\{[^}]{1,60}\}\}", body)
    if leftover:
        raise BuildError(f"{src.name}: unresolved template tokens: {', '.join(sorted(set(leftover)))}")

    body = inject_cover_art(body, cfg)

    # Micro-typography last: it must see the final text, including resolved
    # tokens and labels, and it deliberately skips <pre>/<code>/<svg>.
    try:
        body = typography.apply(body, lang, cfg)
    except ValueError as e:
        raise BuildError(str(e))

    theme_css = gen_theme_css(cfg, root, lang)
    structural = (root / "build" / "style.css").read_text()
    return (
        f'<!doctype html>\n<html lang="{lang}"><head><meta charset="utf-8">\n'
        f"<style>{theme_css}</style>\n<style>{structural}</style></head>\n"
        f"<body>{body}</body></html>"
    )


# ──────────────────────────────────────────────────────────── render + stamp


def find_chrome(cfg: dict) -> str:
    import os

    for candidate in [os.environ.get("ARCH_SPEC_CHROME"), (cfg.get("chrome") or {}).get("path")]:
        if candidate:
            if Path(candidate).exists() or shutil.which(candidate):
                return candidate
            raise BuildError(f"configured Chrome not found: {candidate}")
    for candidate in CHROME_CANDIDATES:
        if Path(candidate).exists() or shutil.which(candidate):
            return candidate
    raise BuildError(
        "no Chrome/Chromium found. Install Google Chrome, or set chrome.path in "
        ".pdf-press.config.json, or export ARCH_SPEC_CHROME=/path/to/chrome"
    )


def render(html: str, out_pdf: Path, cfg: dict, work_dir: Path) -> None:
    chrome = find_chrome(cfg)
    budget = (cfg.get("chrome") or {}).get("virtualTimeBudgetMs", 10000)
    with tempfile.NamedTemporaryFile("w", suffix=".html", dir=work_dir, delete=False, encoding="utf-8") as f:
        f.write(html)
        tmp = Path(f.name)
    try:
        proc = subprocess.run(
            [
                chrome, "--headless=new", "--disable-gpu", "--no-pdf-header-footer",
                f"--virtual-time-budget={budget}",
                f"--print-to-pdf={out_pdf}", tmp.as_uri(),
            ],
            capture_output=True, timeout=180,
        )
    finally:
        tmp.unlink(missing_ok=True)
    if proc.returncode != 0 or not out_pdf.exists():
        tail = (proc.stderr or b"").decode(errors="replace").strip().splitlines()[-8:]
        raise BuildError("Chrome failed to render:\n  " + "\n  ".join(tail))


def stamp(pdf_path: Path, cfg: dict, root: Path, edition: str, lang: str, version: str, pdf_date: str) -> None:
    from reportlab import rl_config

    rl_config.invariant = 1  # deterministic reportlab output — no timestamps, stable IDs

    from pypdf import PdfReader, PdfWriter
    from pypdf.generic import ArrayObject, ByteStringObject
    from reportlab.pdfbase import pdfmetrics
    from reportlab.pdfbase.ttfonts import TTFont
    from reportlab.pdfgen import canvas

    theme = cfg["theme"]
    foot = theme.get("footer") or {}
    ed = cfg["editions"][edition]

    font_name = "Helvetica"
    if foot.get("fontFile"):
        fpath = root / foot["fontFile"]
        if not fpath.exists():
            raise BuildError(f"theme.footer.fontFile: no such file {fpath}")
        font_name = "ArchSpecFooter"
        pdfmetrics.registerFont(TTFont(font_name, str(fpath)))

    page_w, page_h = PAGE_SIZES[(theme.get("page") or {}).get("size", "A4")]
    side = foot.get("sideMarginMm", 16) * MM
    baseline = foot.get("baselineMm", 11) * MM
    size = foot.get("sizePt", 6.4)
    r, g, b = resolve_color(foot.get("color", "dim"), theme)

    left_text = f"{i18n(ed.get('footerLeft'), lang, f'editions.{edition}.footerLeft')} · {version}"
    page_word = i18n((foot.get("pageWord") or {}).get(lang) if foot.get("pageWord") else None,
                     lang, "theme.footer.pageWord", required=False) or ("page" if lang == "en" else lang)

    reader = PdfReader(str(pdf_path))
    total = len(reader.pages)

    buf = BytesIO()
    c = canvas.Canvas(buf, pagesize=(page_w, page_h))
    for i in range(total):
        if i == 0:  # the cover carries its own meta block
            c.showPage()
            continue
        c.setFont(font_name, size)
        c.setFillColorRGB(r, g, b)
        c.drawString(side, baseline, left_text)
        c.drawRightString(page_w - side, baseline, f"{page_word} {i + 1} / {total}")
        c.showPage()
    c.save()
    buf.seek(0)

    overlay = PdfReader(buf)
    writer = PdfWriter()
    # clone_document_from_reader, NOT add_page-in-a-loop: a fresh writer builds its
    # Catalog from pages alone and silently drops the Catalog-level tables — including
    # /Dests, the named-destination map Chromium emits for in-document links. The link
    # ANNOTATIONS live on the pages and survive, so every `href="#id"` becomes a
    # /Dest pointing at a name defined nowhere: a click that does nothing, in a PDF
    # that looks correct in every other respect. (Regression found in review, 2026-08.)
    writer.clone_document_from_reader(reader)
    for i, page in enumerate(writer.pages):
        if i > 0:
            page.merge_page(overlay.pages[i])

    title = i18n(ed.get("title") or cfg["project"].get("title"), lang, "project.title")
    deprecated = " [DEPRECATED]" if ed.get("deprecated") else ""
    writer.add_metadata({
        "/Title": f"{cfg['project']['name']} — {title} ({i18n(ed.get('label'), lang, 'label')}){deprecated}",
        "/Author": cfg["project"].get("author", cfg["project"]["name"]),
        "/Subject": i18n(cfg["project"].get("subject"), lang, "project.subject", required=False),
        "/Keywords": i18n(ed.get("marking"), lang, "marking") + (" · DEPRECATED" if ed.get("deprecated") else ""),
        "/Creator": f"pdf-press pipeline v{PIPELINE_VERSION}",
        "/Producer": f"pdf-press pipeline v{PIPELINE_VERSION}",
        "/CreationDate": pdf_date,
        "/ModDate": pdf_date,
    })

    # Deterministic trailer /ID — pypdf would otherwise seed it from the clock.
    seed = f"{cfg['project']['name']}|{edition}|{lang}|{version}".encode()
    digest = hashlib.md5(seed).digest()
    try:
        writer._ID = ArrayObject([ByteStringObject(digest), ByteStringObject(digest)])
    except Exception:  # noqa: BLE001 — pypdf internal; a nondeterministic ID is not fatal
        print("pdf-press: warning — could not pin the PDF trailer /ID (pypdf internals moved)")

    with open(pdf_path, "wb") as f:
        writer.write(f)


# ──────────────────────────────────────────────────────────── driver


def verify(pdf_path: Path, cfg: dict) -> list[str]:
    """Read back the PDF we just wrote and assert what only the output can tell us.

    lint.py checks inputs; nothing else checks reality. The defect this exists for:
    Chrome cannot embed a VARIABLE font, so a `woff2-variations` face is silently
    outlined into Type3 fonts — the document still looks right while carrying no
    real font, which breaks print preflight and text extraction downstream.
    """
    from pypdf import PdfReader

    problems: list[str] = []
    reader = PdfReader(str(pdf_path))
    want_w, want_h = PAGE_SIZES[(cfg["theme"].get("page") or {}).get("size", "A4")]

    outlined: set[str] = set()
    unembedded: set[str] = set()

    def walk(res, depth: int = 0) -> None:
        if depth > 4 or not res:
            return
        fonts = res.get("/Font")
        if fonts:
            for key, ref in fonts.items():
                try:
                    f = ref.get_object()
                except Exception:  # noqa: BLE001 — a broken ref is not worth failing a build over
                    continue
                # Type3 fonts usually carry no /BaseFont; fall back to /Name, then the
                # resource key, so the warning names something the author can act on.
                name = str(f.get("/BaseFont") or f.get("/Name") or key).lstrip("/")
                subtype = str(f.get("/Subtype", ""))
                if subtype == "/Type3":
                    outlined.add(name)
                    continue
                desc = f.get("/FontDescriptor")
                if desc is not None:
                    d = desc.get_object()
                    if not any(k in d for k in ("/FontFile", "/FontFile2", "/FontFile3")):
                        unembedded.add(name)
        xobjs = res.get("/XObject")
        if xobjs:
            for ref in xobjs.values():
                try:
                    walk(ref.get_object().get("/Resources"), depth + 1)
                except Exception:  # noqa: BLE001
                    continue

    for i, page in enumerate(reader.pages, 1):
        w, h = float(page.mediabox.width), float(page.mediabox.height)
        if abs(w - want_w) > 1 or abs(h - want_h) > 1:
            problems.append(f"page {i} is {w:.0f}x{h:.0f}pt, expected {want_w:.0f}x{want_h:.0f}")
        walk(page.get("/Resources"))

    if len(reader.pages) < 2:
        problems.append("only one page — the cover is there but no content rendered")

    # In-document links: an /Annot whose /Dest names a destination the Catalog does
    # not define is a click that silently does nothing. The HTML side can be perfect
    # (lint proves every href="#id" has a target) and the PDF still ship dead links,
    # because the destination table lives in the Catalog and is easy to drop while
    # post-processing. This is the only layer that can see it.
    try:
        defined = set(reader.named_destinations)
    except Exception:  # noqa: BLE001
        defined = set()
    wanted: set[str] = set()
    for page in reader.pages:
        for annot in page.get("/Annots") or []:
            try:
                obj = annot.get_object()
            except Exception:  # noqa: BLE001
                continue
            if obj.get("/Subtype") != "/Link":
                continue
            dest = obj.get("/Dest")
            # pypdf keys named_destinations WITH the leading slash ("/ref-o16"),
            # exactly as the annotation writes it — compare the raw names.
            if isinstance(dest, str):
                wanted.add(str(dest))
    dead = sorted(n for n in wanted if n not in defined)
    if dead:
        shown = ", ".join(dead[:5]) + (" …" if len(dead) > 5 else "")
        problems.append(
            f"{len(dead)} internal link(s) point at undefined named destination(s) "
            f"({shown}) — the annotations survived a post-processing step that dropped "
            f"the Catalog's /Dests table; clicking them does nothing")
    if outlined:
        # Chrome outlines any face it cannot embed: a VARIABLE webfont
        # ('woff2-variations'), or a system/licence-restricted family when
        # theme.fonts declares no files at all. The page looks correct either way,
        # so this is only visible by reading the output back.
        real = sorted(n for n in outlined if not re.fullmatch(r"F\d+", n))
        shown = ", ".join(real[:6]) + (" …" if len(real) > 6 else "") if real else f"{len(outlined)} face(s)"
        problems.append(
            f"text drawn as Type3 outlines rather than embedded fonts ({shown}) — the PDF "
            f"carries no real font. Cause: a variable webfont, or theme.fonts declaring no "
            f"font files (system faces cannot be embedded). Ship STATIC font files if print "
            f"preflight, PDF/A, or downstream text extraction matter"
        )
    if unembedded:
        problems.append("font(s) referenced but not embedded: " + ", ".join(sorted(unembedded)))
    md = reader.metadata or {}
    if not md.get("/CreationDate"):
        problems.append("no /CreationDate — the deterministic metadata rewrite did not apply")
    return problems


def out_path(cfg: dict, root: Path, edition: str, lang: str, version: str) -> Path:
    out = cfg.get("output") or {}
    pattern = out.get("pattern", "{project}-architecture-{edition}-{lang}.pdf")
    name = pattern.format(project=cfg["project"]["name"], edition=edition, lang=lang, version=version)
    # A superseded edition is marked in the FILENAME, because that is what survives
    # being e-mailed, downloaded and re-shared. Renaming the file by hand does not
    # work — the next build writes the pattern name straight back.
    if cfg["editions"][edition].get("deprecated"):
        suffix = Path(name).suffix
        name = f"{name[: -len(suffix)] if suffix else name}.deprecated{suffix}"
    # Default "." = beside the config, i.e. inside docs/architecture/. ".." would put
    # the PDFs one level above the document set, where nobody looks for them.
    return (root / out.get("dir", ".") / name).resolve()


def build_one(cfg: dict, root: Path, edition: str, lang: str, out_dir: Path | None,
              strict: bool = False) -> Path:
    version, pdf_date = resolve_version(cfg)
    html = assemble(cfg, root, edition, lang, version)
    dest = out_path(cfg, root, edition, lang, version)
    if out_dir:
        dest = (out_dir / dest.name).resolve()
    dest.parent.mkdir(parents=True, exist_ok=True)
    render(html, dest, cfg, root)
    stamp(dest, cfg, root, edition, lang, version, pdf_date)
    pages = len(__import__("pypdf").PdfReader(str(dest)).pages)
    print(f"built {dest.name}  ({pages} pp, {dest.stat().st_size // 1024} kB)")
    problems = verify(dest, cfg)
    for p in problems:
        print(f"  {'ERROR' if strict else 'warn '}  {p}")
    if problems and strict:
        raise BuildError(f"{dest.name}: output verification failed")
    return dest


def all_targets(cfg: dict) -> list[str]:
    return [f"{e}.{l}" for e in cfg["editions"] for l in cfg["languages"]]


def main() -> int:
    ap = argparse.ArgumentParser(prog="build.py", description="Build pdf-press architecture PDFs.")
    ap.add_argument("targets", nargs="*", help="edition.lang pairs; default is every combination")
    ap.add_argument("--config", default=None, help="path to .pdf-press.config.json (default: ../.pdf-press.config.json)")
    ap.add_argument("--out-dir", default=None, help="write PDFs here instead of output.dir (does not touch shipped files)")
    ap.add_argument("--list", action="store_true", help="print the target matrix and exit")
    ap.add_argument("--repro", action="store_true", help="build each target twice and assert byte-identical output")
    ap.add_argument("--strict", action="store_true", help="treat output-verification warnings as errors")
    args = ap.parse_args()

    build_dir = Path(__file__).resolve().parent
    cfg_path = Path(args.config).resolve() if args.config else (build_dir.parent / ".pdf-press.config.json")
    cfg = load_config(cfg_path)
    root = cfg_path.parent

    targets = args.targets or all_targets(cfg)
    if args.list:
        for t in targets:
            e, l = t.split(".")
            print(f"{t:24} -> {out_path(cfg, root, e, l, resolve_version(cfg)[0]).name}")
        return 0

    out_dir = Path(args.out_dir).resolve() if args.out_dir else None
    failed = False
    # An edition need not exist in every language (a supplier spec may be
    # Czech-only). Named targets still fail loudly; the implicit full matrix
    # skips what has no content instead of killing the whole run.
    if not args.targets:
        content_dir = root / (cfg.get("content") or {}).get("dir", "build/content")
        present = []
        for t in targets:
            edition, lang = t.split(".", 1)
            derive = (cfg["editions"][edition].get("content") or {}).get("from")
            if (content_dir / f"{derive or edition}.{lang}.html").exists():
                present.append(t)
            else:
                print(f"pdf-press: skipping {t} — no content file")
        targets = present
    for t in targets:
        if "." not in t:
            raise BuildError(f"target {t!r} is not <edition>.<lang>")
        edition, lang = t.split(".", 1)
        if args.repro:
            with tempfile.TemporaryDirectory() as td:
                a = build_one(cfg, root, edition, lang, Path(td) / "a", args.strict)
                b = build_one(cfg, root, edition, lang, Path(td) / "b", args.strict)
                ha = hashlib.sha256(a.read_bytes()).hexdigest()
                hb = hashlib.sha256(b.read_bytes()).hexdigest()
                if ha == hb:
                    print(f"  repro OK  {t}  sha256 {ha[:16]}")
                else:
                    print(f"  REPRO FAIL {t}  {ha[:16]} != {hb[:16]}")
                    failed = True
        else:
            build_one(cfg, root, edition, lang, out_dir, args.strict)
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
