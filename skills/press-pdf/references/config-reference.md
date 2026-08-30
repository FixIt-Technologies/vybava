# `.pdf-press.config.json` — full key reference

Lives at `docs/architecture/.pdf-press.config.json`. Machine-checkable against the sibling `pdf-press.config.schema.json` (point an editor at it for completion). `lint.py` re-checks what a JSON Schema cannot express — translation coverage, files that must exist, cross-references between editions and figures.

Rule behind every key: **the pipeline holds no project values.** Anything you'd hardcode in `build.py`, `style.css`, or a content file belongs here.

## Top level

| Key | Required | What it controls | Failure mode when wrong |
|---|---|---|---|
| `schemaVersion` | ✔ | Config contract version, currently `1` | Build refuses on a mismatch |
| `pipelineVersion` | ✔ | Which `assets/pipeline/` generation scaffolded this project | Ahead of the pipeline → build refuses and tells you to `upgrade` |
| `project` | ✔ | Cover text and PDF metadata | Missing translation → hard error naming the language |
| `version` | ✔ | Version label + the fixed PDF timestamps | `policy: "date"` silently destroys reproducibility |
| `output` | | Output directory and filename pattern | Defaults to `.` (beside the config) and `{project}-architecture-{edition}-{lang}.pdf` |
| `languages` | ✔ | The language matrix everything must cover | A language here with no content file → lint warning, that target cannot build |
| `editions` | ✔ | One entry per audience | Wrong `figures` list → build refuses the figure (this is the feature) |
| `theme` | ✔ | Every visual token | See below |
| `figures` | | Figure directory, label file, lint strictness | — |
| `content` | | Content directory and the honesty doctrine | Empty `normativeMarkers` for a language → that language is unchecked (lint warns) |
| `chrome` | | Binary path + layout budget | Autodetected; `ARCH_SPEC_CHROME` overrides everything |

## `project`

`name` is a slug used in filenames and on the cover. `title` and `subtitle` are `{ lang: string }` maps and must cover every entry in `languages`. `author` and `subject` land in PDF metadata — `subject` is what a document management system indexes.

## `version`

```json
{ "policy": "config", "value": "v2026.07", "date": "2026-07-26" }
```

`value` is printed on the cover and in every footer. `date` fixes `/CreationDate` and `/ModDate`. Both required under `policy: "config"` — the only reproducible setting. `policy: "date"` derives `vYYYY.MM` from the clock — lint warns, and `--repro` becomes a coin toss across a month boundary.

Bump `value` and `date` together, in the same commit as the content change they describe.

## `output`

`dir` (relative to the config) and `pattern` (`{project} {edition} {lang} {version}`).

**Check where files actually landed on the first build.** `dir` resolves against `docs/architecture/`, so `".."` writes PDFs into `docs/` — where nobody looks. Use `"."`.

For a document set *issued* to counterparties, point `dir` at a dated release folder — `"releases/2026-07-27"` — and bump it when you cut the next release. Each folder holds the full set as it stood that day. Never rebuild into an old folder.

## `editions.<name>`

```json
{
  "label":      { "cs": "partnerské vydání" },
  "marking":    { "cs": "Důvěrné — pro potřeby partnera" },
  "footerLeft": { "cs": "acme — architektura platformy · partnerské vydání" },
  "tagStyle":   "paper",
  "figures":    ["01-system-context", "05-command-lifecycle"],
  "scopeNote":  "Integration-facing only. Omits module breakdown, deployment."
}
```

- `label` → the cover edition tag. `marking` → the cover confidentiality line and the PDF `/Keywords`. `footerLeft` → the stamped left footer on every page after the cover.
- `tagStyle` — `ink` (solid dark), `paper` (outlined), `brand` (brand fill).
- **`figures` is a scope gate, not a manifest.** A `{{fig:…}}` in this edition's content not listed here fails the build with the figure named — the mechanism that stops an internal deployment diagram reaching a partner PDF. Widening the list is a deliberate disclosure decision; make it in a commit of its own.
- `scopeNote` is never rendered — the instruction to whoever authors the next chapter about what this edition deliberately omits.
- `deprecated: true` — the edition is superseded. Its PDF is written as `…-<edition>-<lang>.deprecated.pdf`, metadata carries `[DEPRECATED]`, and it keeps building normally so the content stays reproducible. The **filename** is marked, not just a folder — the name survives being e-mailed and re-shared. Never rename the file by hand — the next build writes the pattern name straight back.
- `title` / `subtitle` — optional per-edition overrides of `project.title` / `.subtitle`, for an edition that is **a different document** sharing the pipeline and identity. Omit → project's own title. `subtitle` is only consulted when `title` is also set, so an edition cannot silently inherit a mismatched pair.

A language is **not** required to exist for every edition. Bare `build.py` skips `<edition>.<lang>` pairs with no content file and prints what it skipped; naming such a target explicitly still fails loudly. This lets a Czech-only supplier spec live beside a bilingual architecture document.

### Derived editions — one tagged master, N audience renderings

```json
"external": {
  "label":   { "cs": "externí vydání" },
  "marking": { "cs": "Důvěrné — pro dodavatele" },
  "footerLeft": { "cs": "…" },
  "figures": ["11-control-chain", "12-priority-stack"],
  "content": { "from": "internal-spec", "stripStatus": true,
               "dropClasses": ["internal-only"] }
}
```

An edition with a `content` block has **no content file of its own** — it renders another edition's file, transformed at build time:

- `from` — the source edition (its `<from>.<lang>.html` is read instead).
- `stripStatus` — removes every `data-status` / `data-source` attribute; the audience reads clean normative prose while the master stays lint-truthful.
- `dropClasses` — removes top-level `<section>` elements carrying any of these classes. **Sections only, no nesting**: an inline internal-only fragment must live in its own section or it will not be dropped.

Two content files saying the same thing drift on the first edit — author the honest badged version once; the external edition is a projection. The `figures` scope gate still applies to the derived edition, and lint judges it against the *post-transformation* body — an internal-only figure appearing only inside a dropped section is not a violation.

Verify a derived edition by reading its PDF, not the master's: a section you meant to drop but placed inside another section will silently ship.

## `theme`

| Key | Notes |
|---|---|
| `page.size` | `A4` or `LETTER`. The footer stamp derives its geometry from this — they cannot disagree. |
| `page.margin` | CSS `@page` shorthand. Bottom margin must leave room for the stamp (`footer.baselineMm`). |
| `contentWidth` | Figure `viewBox` width, in SVG units, equal to the printable width. All cross-figure consistency hangs off this number. |
| `colors` | Structural palette; each key becomes `--key`. Required: `paper panel ink body dim faint edge brand`. Optional, used by the stylesheet when present: `panel-2 edge-strong brand-wash brand-ink`. |
| `signals` | Semantic figure palette, `name: [stroke, wash]` → `--name` and `--name-wash`. The only colours a figure may use beyond the structural palette. |
| `fonts.{body,display,mono}` | `family`, optional `stack` fallback, `faces[]` with paths relative to the config. `mono` carries all figure text and every mono UI element — it must have the diacritics your languages need. |
| `scale` | Type scale in pt, merged over pipeline defaults. Keys: `body lead small h2 h3 h4 coverTitle coverSubtitle table pre kicker figcaption`. |
| `cover.style` | `hero-fill` (full-bleed brand + motif streaks), `panel` (paper + brand rule), `minimal` (type only). |
| `cover.motif` | Short glyph run (e.g. `///`) used as the kicker prefix and on the edition tag. Empty disables it. |
| `cover.heightMm` | Optional. Defaults to 1 mm under the **resolved page height**, following `page.size`. A value that does not fit is clamped with a warning — an unclamped 296 mm cover on LETTER spills onto page 2, puts the meta block in the top margin, and shifts every page number. |
| `logo` | SVG path, inlined on the cover and in the endmark. Omit for a type-only document. Exempt from figure lint. |
| `footer.fontFile` | TTF for the stamp. **Set it.** The Helvetica fallback cannot render every Czech diacritic, and the stamp is the one text the browser never sees. |
| `footer.pageWord` | The word before `3 / 18`, per language. Unset for a non-`en` language → footer reads `cs 3 / 18`. Lint checks every character against the stamp font. |
| `footer.color` | Prefer a **`theme.colors` key** (e.g. `"dim"`). A raw `[r,g,b]` triple works but is an unvalidated fourth colour source — lint warns. |

Font `faces[]` are emitted as `@font-face` with absolute `file://` URIs, so they resolve regardless of where the temporary render HTML lands. A missing font file is a hard error, not a silent fallback.

## `figures.lint`

| Key | Default | Meaning |
|---|---|---|
| `enforceViewBoxWidth` | `true` | Fail a figure whose `viewBox` width ≠ `theme.contentWidth` |
| `rx` | `4` | Required corner radius on box rects (width ≥ 40) |
| `strokeWidth` | `1.2` | Box stroke weight; other weights warn |
| `fontSizes` | `[11, 9.5, 8]` | The only permitted `font-size` values: group title / body label / annotation |
| `minRenderedPt` | `6.0` | Readable floor **on paper**. A figure is downscaled to the text column, so 8 units at `contentWidth` 760 prints at ~5.07 pt. Lint computes the fit factor and warns — enforcing the authored value alone misses this |
| `allowExtraColors` | `[]` | Escape hatch. Every entry needs a reason in the commit message, or the palette stops meaning anything |

## What lint checks beyond the schema

Each check caught a real defect:

| Check | Catches |
|---|---|
| translation coverage across `languages` | a language added to the matrix but not to the strings |
| referenced files exist (fonts, logo, stamp TTF) | a config that renders as a silent fallback |
| **multi-face roles declare `unicodeRange`** | two faces both covering the whole plane, so glyph coverage depends on browser fallback |
| **glyph coverage per role** | a face with no glyph for a Czech diacritic used on the cover |
| **stamp font covers every footer string** | blanked diacritics in the page footer — reportlab has no fallback |
| **`footer.color` names a palette key** | a stamp colour outside the checked palette |
| **figure sizes clear `minRenderedPt`** | 5 pt figure text |
| figure palette / sizes / `rx` / `viewBox` width | a figure that stops matching the set |
| **per-edition** figure usage | an edition declaring a figure its own content never uses (a unioned check hides this) |
| **the managed stylesheet holds no literals** | a `pt` size or hex no project can retune from config |
| normative wording without `data-status` | a document promising behaviour that does not exist |

`build.py` adds a post-render pass over the PDF it just wrote — page geometry, page count, deterministic metadata, embedded-vs-outlined fonts. Input linting cannot see those.

## `content.doctrine`

| Key | Default | Meaning |
|---|---|---|
| `statusTags` | `["IMPLEMENTED","PLANNED","PROPOSED"]` | Allowed `data-status` values |
| `requireStatusOnNormative` | `true` | A block containing a normative marker must carry `data-status` |
| `requireSourceOnStatus` | `true` | `IMPLEMENTED` must also carry `data-source` with a repo path |
| `normativeMarkers` | per language | The words that make a sentence a promise. Extend per project and per language — a language with no markers is silently unchecked (lint warns) |

Czech starter set: `musí`, `nesmí`, `je povinen`, `zaručuje`, `vyžaduje`. English: `must`, `shall`, `may not`, `is required to`, `guarantees`. Add domain verbs your specs actually use (`garantuje`, `odmítne`, `přebíjí`).

## Adding a language

1. Add the code to `languages`.
2. Add the translation to every i18n map: `project.title`, `project.subtitle`, `project.subject`, each edition's `label` / `marking` / `footerLeft`, `theme.footer.pageWord`.
3. Add the language to **every key** in `figures/labels.json`.
4. Add `content.doctrine.normativeMarkers.<lang>`.
5. Create `build/content/<edition>.<lang>.html` per edition.
6. `lint.py` enumerates whatever you missed — run it before writing prose.
