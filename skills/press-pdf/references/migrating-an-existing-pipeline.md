# Migrating a project that already hand-rolled a PDF build

Goal: **identical-looking output from a config**, so the visual identity survives and every future change becomes a token edit. Do it in this order; each step is independently verifiable.

## 0. Do not migrate blind

Build the project's existing PDFs first and keep them — the reference the migration is judged against: page count, page breaks, cover, figure scale.

## 1. Transcribe the identity into `.pdf-press.config.json`

Read the existing pipeline's stylesheet `:root` block, its footer table, and its figure conventions doc; copy the values into `theme`. Invent nothing at this step — every colour, font path, margin, and footer string already exists somewhere.

`assets/examples/example.pdf-press.config.json` is exactly this transcription for one real project: `build/style.css` `:root` → `theme.colors` + `theme.signals`; `build.py`'s `FOOTER` dict → each edition's `footerLeft` and `marking`; `figures/_conventions.md` → `theme.contentWidth` and `figures.lint`. Use it as the worked example.

## 2. Tokenize the content

The only step with real editing. Hardcoded → token:

| Hardcoded | Becomes |
|---|---|
| The document title and subtitle in the cover markup | `{{title}}` / `{{subtitle}}` |
| `class="edition-tag internal"` (edition name as a CSS class) | `class="edition-tag {{editionTagStyle}}"` |
| The confidentiality line | `{{marking}}` |
| The version string, if not already a token | `{{version}}` |
| An inlined `<svg>` logo | `{{logo}}` |
| Per-edition footer text inside the content | nothing — the stamp owns it |

Tokenizing stops the content files disagreeing with each other; a new edition becomes a config entry, not a stylesheet change.

## 3. Swap the managed files

Replace the project's `build.py` and `style.css` with the skill's, keeping `content/` and `figures/` untouched. Delete the bespoke script only after step 5 passes — until then it is the fallback.

Rules the old stylesheet carried that the structural sheet lacks: add them to the skill's `style.css` if general, or express them with existing classes if not. Never a per-project `custom.css` fork — that is how two projects silently diverge.

## 4. Lint, and expect errors — then decide which side is wrong

The first `lint.py` run on a pre-existing figure set reports violations by the hundred (~150–200 on a ten-figure set). They were always inconsistencies, previously invisible.

**Do not start editing figures.** Sort the errors by kind first (`lint.py | grep -o 'font-size [0-9.]*' | sort | uniq -c`, same for `rx=` and `color #…`), then ask per kind: *is the figure set wrong, or is the default config wrong for this project?*

- A **broad, consistent** practice — ten figures using a 15-value type scale, every box at `rx="3"`, a documented neutral/dim/edge palette — is the project's real convention. Transcribe it into `figures.lint` (`fontSizes`, `rx`, `allowExtraColors`). Rewriting 200 attributes to match a default invented at migration time changes how every figure looks, and the step-0 reference PDFs no longer match.
- A **handful of outliers** — three `rx="5"` among fifty `rx="3"` — is the actual defect. Normalise those.

The distinction is *stated convention vs incidental drift*; `figures/_conventions.md` (or its equivalent) usually settles it in one read. Record in the commit message which values you transcribed and why, so the next author doesn't "fix" them back.

`allowExtraColors` gets the same treatment: a neutral ink/dim/edge/panel family used by every figure is a palette the config failed to declare, not a violation. A single off-palette accent in one figure is a violation.

## 5. Compare against the reference

Rebuild with `--out-dir` into a scratch directory so the shipped PDFs stay untouched, then compare to the originals: page count first, then the cover, then each figure page, then the last page of the longest chapter. Expect small text-reflow differences where the type scale was hardcoded at values the config rounds — decide per case whether to match the old value exactly (put it in `theme.scale`) or accept the new one.

Then `--repro` to confirm the migrated build is reproducible.

## 6. Commit as its own change

The migration commit contains no content edits beyond tokenization — content changes in the same commit make a rendering regression indistinguishable from an authoring change.
