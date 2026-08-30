# Determinism — what byte-identity depends on

`build.py --repro` builds each target twice into separate directories and compares sha256. Reference project: passed at 8 pages, 598 kB.

## Sources of nondeterminism, all handled

| Source | What it does | Fix in `build.py` |
|---|---|---|
| Version derived from the clock | `date.today()` → the label changes every month | `version.policy: "config"` — label and dates come from the config |
| PDF `/CreationDate` + `/ModDate` | Chrome stamps the render moment | Overwritten from `version.date` |
| reportlab document ID + timestamps | The footer overlay carries its own | `rl_config.invariant = 1` before any reportlab import that matters |
| pypdf trailer `/ID` | Seeded nondeterministically | Pinned to `md5(project\|edition\|lang\|version)` |

## What must be held constant

- **The same Chrome/Chromium build.** Chrome's PDF writer (Skia) is the layout and font-subsetting engine; a browser update can legitimately change output bytes. Pin the binary in `chrome.path`, or treat a Chrome upgrade as a content-neutral rebuild you must re-verify.
- **The pinned Python deps** in `requirements.txt`. A pypdf or reportlab minor bump can reorder objects. Bump deliberately, then re-run `--repro` on every target.
- **The same font files.** A refreshed webfont changes the subset and every glyph offset.
- **`version.policy: "config"`.** Under `"date"` there is nothing to verify.

## When `--repro` fails

Work down this list — ordered by likelihood:

1. **`version.policy` is `"date"`.** Lint warns. Switch to `"config"`.
2. **`version.date` is missing.** The build refuses, but an older config may have slipped through — check both `value` and `date` are set.
3. **A content or figure file changed between the two builds** (editor autosave). Re-run on a clean tree.
4. **Chrome was upgraded mid-session.** Compare `chrome --version` against the last known-good build; the byte diff will be large and font-table-shaped.
5. **A dep drifted.** `.venv/bin/pip freeze | diff - requirements.txt`.
6. **The pypdf `/ID` pin stopped applying.** The build prints `warning — could not pin the PDF trailer /ID` when pypdf's internals move; the diff is tiny and confined to the trailer. Fix the pin in `build.py` in the skill, not in the project.
7. **A figure or the logo is a symlink to something that changed.** Resolve and copy.

To see *what* differs:

```bash
# object-level diff
.venv/bin/python -c "
from pypdf import PdfReader
a, b = PdfReader('a.pdf'), PdfReader('b.pdf')
print(len(a.pages), len(b.pages))
print(a.metadata)
print(b.metadata)
"
# byte-level first divergence
cmp a.pdf b.pdf
```

A difference confined to the trailer is cosmetic. A difference in a content stream or a font table means the render genuinely changed — find out why before shipping.

## The output-verification pass

Reproducibility says the bytes are stable, not that they are right. `build.py` reads back every PDF it writes and reports:

- page geometry against `theme.page.size`, and a page count above 1;
- deterministic metadata actually applied (`/CreationDate` present);
- **fonts embedded vs outlined into Type3** — the one defect no input check can see. A variable webfont, or a config with no font files, yields a page that looks perfect and carries no real font. `--strict` turns these warnings into a non-zero exit for CI.

## What determinism does NOT give you

Not correctness, not a figure fitting its frame, not a table unsplit mid-row, not an arrow avoiding a label, not a diacritic surviving into the footer stamp — Chrome renders a broken layout identically forever. **Read the rendered pages**, and for a document that will be signed, run the adversarial audit in SKILL.md Phase 4b.
