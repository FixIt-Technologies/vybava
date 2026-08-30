#!/usr/bin/env python3
"""pdf-press — language-aware micro-typography applied at build time.

MANAGED FILE — scaffolded by the `pdf-press` skill. Edit the project's
.pdf-press.config.json instead; run `/pdf-press upgrade` to re-sync this file.

Technical prose in an inflected language breaks badly at the line ends, and no
amount of authoring discipline fixes it: the author cannot see where a line will
break, and hand-typing `&nbsp;` through a 20-page document is both unreadable in
the source and forgotten on the next edit.

Czech typography (ČSN 01 6910) forbids exactly the breaks that a spec full of
identifiers produces:

  "…povel s        →  a one-letter preposition stranded at the line end, with
   externalKey…"      the identifier alone on the next line

  "mrtvé pásmo 0,5  →  a value split from its unit
   kW"

  "požadavek < 5    →  a comparison split from its number
   s"

So the pipeline inserts the non-breaking spaces itself, at assembly time, over
TEXT NODES ONLY — never inside <pre>, <code>, <svg>, tags or attributes.

Rules are per language and configured under `content.typography`. Everything is
a pure string transform, so it stays byte-reproducible.
"""

from __future__ import annotations

import re

NBSP = " "

# Elements whose text is verbatim (code, figure labels) — never touched.
SKIP_TAGS = {"pre", "code", "script", "style", "svg"}

# ── individual rules ────────────────────────────────────────────────────

# ČSN 01 6910: a one-letter preposition or conjunction may not end a line.
# Czech: k s v z o u i a — plus their capitals. Only when a word follows.
_PREPOSITIONS = re.compile(r"(?<![^\s(\[„\"'—-])([aikosuvzAIKOSUVZ])[ \t]+(?=[\w„\"'(<])")

# A number and its unit are one typographic object. The unit list is
# deliberately explicit: a greedy \w+ would bind "5 povelů" too.
_UNITS = (
    r"kWh|MWh|kVA|kWp|kW|MW|Wh|W|V|A|Hz|kHz|"
    r"ms|s|min|h|d|%|‰|°C|K|"
    r"kB|MB|GB|TB|B|px|pt|mm|cm|m|km|Kč|EUR"
)
# The lookahead rejects a longer word starting with the unit's letters ("5 sekund"
# must not bind), but must NOT reject punctuation: "< 5 s/< 10 s" and "15 min."
# are exactly the places a value gets torn off its unit.
_NUM_UNIT = re.compile(rf"(\d)[ \t]+({_UNITS})(?!\w)")

# ČSN 01 6910: the thousands separator is a NON-BREAKING space. Without this a
# priced document tears an amount in half at the column edge — "je 11 / 000 Kč".
# `_NUM_UNIT` cannot catch it: it binds the LAST group to its unit ("000 Kč"),
# leaving the break between "11" and "000". The lookahead requires exactly a
# three-digit group so "5 povelů" and "2 dny" are untouched; repeated groups
# ("1 234 567") bind in one pass because the matches do not overlap.
_DIGIT_GROUP = re.compile(r"(?<=\d)[ \t]+(?=\d{3}(?!\d))")

# "≤ 1 s" / "< 5 s": bind the operator to its number, and normalise the gap so
# the document does not mix "≤1 s" with "< 2 s" on the same page.
_OPERATOR = re.compile(r"([<>≤≥±~])[ \t]*(\d)")

# An em dash may not start a line — bind it to the word before it.
_DASH = re.compile(r"[ \t]+([—–])[ \t]+")

# Reference + number: kap. 7, § 4.11, obr. 3, tab. 2, str. 15, č. 24, v1.1
_REFERENCE = re.compile(
    r"\b(kap|obr|tab|str|č|čl|odst|písm|s|fig|sec|ch|p)\.[ \t]+(?=[\dIVXA-Z])", re.IGNORECASE)
_SECTION_SIGN = re.compile(r"(§|#)[ \t]+(?=\d)")

# A single- or two-letter word before an inline identifier: the chip cannot
# break internally, so a break in front of it strands the little word.
_BEFORE_CODE = re.compile(r"(?<=[\s>])(\w{1,2})[ \t]+(?=<code[ >])")

RULES = {
    "prepositions": lambda t: _PREPOSITIONS.sub(rf"\1{NBSP}", t),
    "units": lambda t: _NUM_UNIT.sub(rf"\1{NBSP}\2", t),
    "digitGroups": lambda t: _DIGIT_GROUP.sub(NBSP, t),
    "operators": lambda t: _OPERATOR.sub(rf"\1{NBSP}\2", t),
    "dashes": lambda t: _DASH.sub(rf"{NBSP}\1 ", t),
    "references": lambda t: _SECTION_SIGN.sub(rf"\1{NBSP}", _REFERENCE.sub(rf"\1.{NBSP}", t)),
}

# Applied to the markup rather than to a text node, so it lives outside RULES.
MARKUP_RULES = {"beforeCode"}

DEFAULT_RULES = {
    "cs": ["prepositions", "units", "digitGroups", "operators", "dashes", "references", "beforeCode"],
    "sk": ["prepositions", "units", "digitGroups", "operators", "dashes", "references", "beforeCode"],
    "pl": ["prepositions", "units", "digitGroups", "operators", "dashes", "beforeCode"],
    "_": ["units", "digitGroups", "operators", "dashes", "beforeCode"],
}


def rules_for(cfg: dict, lang: str) -> list[str]:
    conf = (cfg.get("content") or {}).get("typography")
    if conf is None:
        return DEFAULT_RULES.get(lang, DEFAULT_RULES["_"])
    if lang in conf:
        return list(conf[lang])
    return list(conf.get("_", DEFAULT_RULES.get(lang, DEFAULT_RULES["_"])))


_TAG = re.compile(r"<(/?)([a-zA-Z][\w:-]*)([^>]*)>|<!--.*?-->", re.DOTALL)


def apply(html: str, lang: str, cfg: dict) -> str:
    """Return `html` with the language's typography rules applied to text nodes."""
    names = rules_for(cfg, lang)
    text_rules = [RULES[n] for n in names if n in RULES]
    unknown = [n for n in names if n not in RULES and n not in MARKUP_RULES]
    if unknown:
        raise ValueError(f"content.typography[{lang!r}]: unknown rule(s) {', '.join(unknown)} "
                         f"(have {', '.join(sorted(RULES) | {'beforeCode'})})")

    if text_rules:
        out: list[str] = []
        pos = 0
        skip_depth = 0
        for m in _TAG.finditer(html):
            chunk = html[pos:m.start()]
            if chunk:
                out.append(chunk if skip_depth else _transform(chunk, text_rules))
            out.append(m.group(0))
            pos = m.end()
            if m.group(2):  # a real tag, not a comment
                tag = m.group(2).lower()
                if tag in SKIP_TAGS and not (m.group(3) or "").endswith("/"):
                    skip_depth += -1 if m.group(1) else 1
                    skip_depth = max(0, skip_depth)
        tail = html[pos:]
        if tail:
            out.append(tail if skip_depth else _transform(tail, text_rules))
        html = "".join(out)

    if "beforeCode" in names:
        html = _BEFORE_CODE.sub(rf"\1{NBSP}", html)
    return html


def _transform(text: str, text_rules: list) -> str:
    for rule in text_rules:
        text = rule(text)
    return text


def report(html: str, lang: str, cfg: dict) -> list[str]:
    """What `apply` would change, as human-readable counts — for lint/diagnostics."""
    names = rules_for(cfg, lang)
    findings = []
    for name in names:
        if name in RULES:
            before = html
            after = apply(html, lang, {"content": {"typography": {lang: [name]}}})
            n = sum(1 for a, b in zip(before, after) if a != b)
            if n:
                findings.append(f"{name}: {n} non-breaking space(s)")
    return findings
