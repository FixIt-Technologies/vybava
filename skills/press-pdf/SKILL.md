---
name: press-pdf
description: "Build offer, documentation (internal+external), and legal PDFs into ~/Exports/<project>/pdf/ from a per-project JSON config — themed, linted, reproducible. Press skill family."
disable-model-invocation: true
---

# press-pdf — offers, documentation, and legal PDFs

Hand-drawn SVG figures, per-scenario endpoint and payload tables, one edition per audience, one file per language — rendered from a single per-project config.

Three guaranteed properties:

| Property | Guaranteed by |
|---|---|
| **Consistent design** | Every colour, font, size, and page geometry is a token in `.pdf-press.config.json`; `lint.py` fails the build on a figure outside the palette, the type scale, or the content width. |
| **Deterministic output** | No wall-clock anywhere: version and PDF dates come from the config, reportlab runs in invariant mode, trailer `/ID` derived from content. `build.py --repro` asserts byte-identity. |
| **Factual content** | Every normative sentence carries `data-status`; `IMPLEMENTED` also carries `data-source` with a repo path. Lint rejects an unmarked "must". |

## Press family (READ FIRST — overrides paths below)

Part of the **press skill family** with `press-logo`. Shared law: `press doctrine` (`press doctrine --schema` for the config schema). State goes through the `press` CLI — never hand-edit `.press.conf.json` / PRESS.md's autogen block.

**Phase −1, before anything else:**

1. `press resolve` → project name (git repo name). Outside git the CLI refuses — **ask the user** which project, then pass `--project` everywhere. `press init` (idempotent) ensures `~/Exports/<project>/` exists.
2. **Document type.** If the prompt didn't name it, ASK: `offer`, `documentation`, or `legal`.
   - `documentation` ALWAYS builds two editions of the same sources: **internal** (our devs/company) and **external** (public — no private information, no internal know-how; enforce via the edition scope gates).
   - `legal` (documents for signature) ALWAYS asks: **who is the issuer and who is the target?** Given an IČO, run `press ares <IČO>` — company name, address, DIČ come from the ARES registry (cached in config), never retyped.
   - `offer` asks for the target company if not stated.
3. **Home remap.** Pipeline home is `~/Exports/<project>/pdf/<type>/<slug>/` (e.g. `pdf/offer/acme-q3/`). Wherever this document or `references/` says `docs/architecture/`, read that home instead — config `.pdf-press.config.json`, `build/`, `content/`, `figures/`, `fonts/`, and rendered `dist/` all live there. Sources and outputs both live in Exports; venvs are disposable and never migrated.
4. **After every successful build:** register the artifact — `press index add --kind pdf --type <type> --file pdf/<type>/<slug>/dist/<file>.pdf --title "…" --version <n> [--issuer …] [--target …] --status draft` — keep the sidecar note's `supersedes: [[…]]` chain honest when versioning, and run `press lint --fix`. Status lifecycle: draft → sent → signed/published.
5. **Pre-family projects** (config found in a repo's `docs/architecture/`): offer a migration — move the build dir under `~/Exports/<project>/pdf/<type>/<slug>/`, recreate the venv, register with `press index add`. Never migrate silently.

## When NOT to use this skill

- Diagram on a shareable board → `/vitrinka:diagram` · living repo documentation → `/vitrinka:docs` · interactive HTML report → `/vitrinka:artifact` · open research question → `/research`.
- Signal/point list or FAT/SAT protocol for an industrial controller supplier — a different artifact class. See `references/content-doctrine.md` § Supplier annexes first.

## Modes

Invoked as `/press-pdf [mode] [args]`. No mode → infer: no config → `setup`; config present → `build`.

| Mode | Does |
|---|---|
| `setup` | Interview, scaffold `docs/architecture/`, write the config, smoke-build a real PDF |
| `build [targets]` | Lint, then render — all editions × languages, or the named `edition.lang` pairs |
| `lint` | Config + figure + label + content checks, no render |
| `figure <slug>` | Author a new conventions-compliant SVG figure and register its labels |
| `edition <name>` | Add an audience edition (scope gate + footer + cover tag), standalone or derived |
| `lang <code>` | Add a language across the config, labels, and content |
| `upgrade` | Re-sync the managed pipeline files when the skill is newer than the project |
| `doctor` | Diagnose a broken setup (venv, Chrome, fonts, drift) without changing content |

## Phase 0 — Detect

1. Walk **CWD → git root** for `docs/architecture/.pdf-press.config.json`; also accept an explicit path argument.
2. Compare its `pipelineVersion` to `PIPELINE_VERSION` in `assets/pipeline/build.py`. Project behind → offer `upgrade` first; project ahead → stop, the skill is stale.
3. **No config → Phase 1 (setup).** Never scaffold into a repo the user did not name; never assume `docs/architecture/` is the right home in a monorepo — confirm the app subdirectory first.
4. Missing venv at `docs/architecture/build/.venv` → create it and install `requirements.txt` before any build.

## Phase 1 — Setup (only when there is no config)

Ends with a real PDF on disk, or the phase failed.

1. **Harvest facts before asking anything.** Read the repo's `README.md`, `CLAUDE.md`, `docs/`, and any design tokens (`design-system.md`, `tailwind.config.*`, CSS custom properties, an existing brand SVG). Palette and fonts come from what the project already uses — **never invent a brand colour**; if nothing exists, say so and use the neutral default.
2. **One batched `AskUserQuestion`** for only what the repo cannot answer: audiences (editions), languages, cover treatment (`hero-fill` / `panel` / `minimal`), whether a logo SVG + brand fonts exist to point at.
3. **Scaffold** — copy from this skill's `assets/`:
   - `assets/pipeline/{build.py,lint.py,pagecheck.py,typography.py,style.css,requirements.txt}` → `docs/architecture/build/`
   - `assets/pdf-press.config.schema.json` → `docs/architecture/`
   - `assets/pdf-press.config.default.json` → `docs/architecture/.pdf-press.config.json`, then fill in the harvested + answered values
   - `assets/templates/figures/*` → `docs/architecture/figures/`
   - `assets/templates/content/internal.cs.html` → `docs/architecture/build/content/`, renamed to the first real `<edition>.<lang>.html`
   - Fonts are the project's own; copy to `docs/architecture/fonts/`, reference by relative path. **Set `theme.footer.fontFile`** — the Helvetica fallback cannot render every Czech diacritic in the page-number stamp.
4. **Write `docs/architecture/README.md`** — rebuild command, managed-file list, one-line "edit the config, not the pipeline" warning.
5. **Verify**: `lint.py` clean → `build.py` → `build.py --repro`. Report page count and sha256, then read the rendered pages (Phase 4).

## Phase 2 — Author content

Content lives in `build/content/<edition>.<lang>.html` as a body fragment: no `<!doctype>`, no `<head>`, no `<style>`. Structure, tokens, and the status vocabulary: `references/content-doctrine.md` — read it before writing a chapter.

Rules that bite:

- **Endpoint and payload tables are generated from the machine-readable contract** (`openapi.json`, `asyncapi.yaml`, JSON Schema), never retyped — and **diff the artifact against the router/publisher before generating from it**; a stale spec yields endpoints that 404. If no artifact exists, say so and treat authoring one as part of the job.
- **Cross-references become links, and links get verified.** `see O16` is a jump target: anchor the registry row (`<tr id="ref-o16">`) and link every mention. Endpoint chips deep-link into the live API reference, generated from the same contract. Lint checks shape and dead internal anchors (including in derived editions, which can drop a link's target); **reachability is a live check run before issuing, with the audience's own credentials** — `references/linking.md`.
- **JSON examples ship in canonical 2-space form**, produced by round-tripping the value (`json.dumps(obj, ensure_ascii=False, indent=2)`), never hand-wrapped to fit the column.
- **Two editions = one master + a derived edition**, never two content files. Author the `data-status`-tagged version, put internal material in **top-level** `internal-only` sections (not nested inside a chapter section), and let `editions.<name>.content` (`from` / `stripStatus` / `dropClasses`) project the external one at build time.
- **Non-breaking spaces are inserted by `typography.py`** at build time (one-letter prepositions, value+unit, `< 5 s`, dashes, `kap. 7`, short words before an inline identifier) — never hand-type `&nbsp;`. **Keep technical vocabulary and status badges in English by default**: readers grep for `SETPOINT_GRID` and `payload`. Use `content.glossary` to enforce consistency in whichever direction the project chose.
- **Every normative sentence needs `data-status`**; `IMPLEMENTED` also needs `data-source="path/to/file.go"` (optionally `:line`). This lets a document specify unbuilt behaviour without lying.
- **No hex codes, no font names, no `pt` sizes in content** — use `style.css` classes and CSS variables. `lint.py` holds the managed stylesheet to the same rule.
- **Never type a chapter, section, or figure number.** Leave `<span class="no"></span>` and `<span class="fig-n"></span>` empty; CSS counters fill them — two editions with different chapters WILL disagree with hardcoded numbers. Only a non-empty span is left alone, so migrated content keeps working.
- **`{{fig:slug}}` only for figures listed in `editions.<edition>.figures`** — that allowlist is the scope gate stopping an internal figure reaching an external edition. Widening it is an audience decision, not a build fix.
- Comments are stripped before token resolution, so a `{{t:key}}` inside a comment is inert.

## Phase 3 — Figures

`references/theme-and-figures.md` holds the conventions and exact lint rules. Non-negotiables: `viewBox` width equals `theme.contentWidth` (never draw at another width and scale in CSS), `rx` from config on every box, stroke width from config, only the configured `font-size` values, only palette colours, every language-specific string as a `{{t:key}}` token in `labels.json` with every configured language present. If a project already has documented figure conventions, transcribe them into `figures.lint` rather than redrawing the set to match defaults.

**Orientation is decided before drawing.** The `viewBox` width is fixed and the page is portrait, so anything that grows with item count — timeline, stage pipeline, checkpoint sequence — goes on a **vertical axis with labels beside it**; horizontal versions collide their own captions once labels are real sentences, and nothing in the pipeline can see inside a figure. `references/theme-and-figures.md` § Orientation.

Signal colours carry meaning, never decoration — reuse the project's semantic assignment (pv / battery / grid / backup / alarm), never pick per figure.

## Phase 4 — Verify (never skip)

Four layers — each catches what the others cannot:

```bash
cd docs/architecture/build
.venv/bin/python lint.py --strict       # INPUTS: config, figures, labels, doctrine, fonts
.venv/bin/python build.py               # render + read the PDF back
.venv/bin/python pagecheck.py           # OUTPUT geometry: how the text fell on paper
.venv/bin/python build.py --repro       # assert byte-identical rebuild
```

`pagecheck.py` reads the rendered PDFs with pdfminer.six and reports what only the output shows: stranded heading at a page foot, chapter tail on a 6%-full page, widow line, caption orphaned from its figure, table header alone above the break, text outside the column or colliding with the footer stamp. Margin and footer violations are ERRORS; judgement calls are warnings (a short page can be a deliberate section end). `--strict` makes everything fatal (the CI setting); `-v` prints every page's fill ratio. Tune per project under `content.pagination` (`minFillRatio`, `minLastPageFill`, `maxLinesAfterHeading`, `marginTolerancePt`, `widowGapFactor`) — loosening a threshold to silence a warning is not a fix; neither is deleting meaning-bearing text instead of reflowing.

`build.py` verifies its own output after every render: page geometry, page count, deterministic metadata, and whether text is backed by embedded fonts or outlined into Type3 (`references/theme-and-figures.md` § Font embedding — decide whether preflight, PDF/A, or text extraction matter for this deliverable rather than dismissing the warning).

**Never pipe `build.py` through `grep`.** The pipeline's exit status becomes grep's, and a `BuildError` on the third of six targets prints a message the filter drops. Read the full output, or check `$?`.

Then **read the rendered PDF pages** with the Read tool's `pages` parameter — cover, one figure page, one table page, and the last page of the longest chapter, in the longest language. Exit code 0 says nothing about a label kissing a box edge, an arrow crossing text, or a legend colliding with a bar.

Report page count and sha256 per target. If `--repro` fails, `references/determinism.md` lists every known cause in order of likelihood.

### The layer rule — verify a claim in the artifact that ships it

Every layer can be green while the deliverable is broken. **A feature that renders into the PDF is not verified until checked in the PDF** — post-processing steps (stamping, merging, linearising, compressing) drop what they were not told to carry, typically document-level structure (outline, named destinations, metadata, attachments) while leaving the page-level evidence that something used to point at it.

| Claim | Verified in | Not sufficient |
|---|---|---|
| internal jump links work | the PDF: every `/Link` annotation's `/Dest` resolves in the Catalog (`build.py` checks this) | lint proving each `href="#id"` has a target |
| external links resolve | a live request as the AUDIENCE, before issuing | the URL being well-formed |
| a deep link scrolls to an operation | the target app: fragment enumerated from the deployed page, scroll asserted | the fragment matching a locally-generated spec |
| a figure is readable | the rendered page, read as an image, in the longest language | the SVG passing lint |
| the numbers are current | the generated artifact AND the prose AND the PDF agreeing | the generator being correct |

### Phase 4b — Adversarial audit (for a document that will be signed)

Before a spec goes to a supplier, client, or auditor, run a measurement-based audit — batched, not one agent per page (global orchestration rules):

| Auditor | Measures | Catches |
|---|---|---|
| Geometry | text bboxes vs the margin box, pairwise text overlap, figure extents vs frame, row integrity across page breaks, orphans/widows | overflow, collisions, mid-row splits |
| Typography | embedded font inventory, glyph attribution for diacritics, rendered size set vs the scale, colour inventory vs palette, cs/en structural parity | fallback faces, dead scale tokens, off-palette colour, language drift |
| Visual | renders every page to PNG and **reads them** | label-vs-edge crowding, arrows through text, ugly breaks, a cover that reads cheap |

**The audit is a gate, not a parallel task.** Do not issue, merge, or send while it runs, and do not substitute your own spot-check. If the schedule cannot absorb the wait, start the audit earlier.

Require each finding classified `PIPELINE` (any project inherits it → fix in the skill) vs `CONTENT` (this document only), with severity and measured evidence. A finding the auditor could not measure is a suggestion, not a defect. Findings `lint.py` *should* have caught become new lint rules.

## Known limitations

- **No TOC page numbers.** Chrome's print pipeline has no `target-counter`. Omit them or implement a two-pass build (render, extract heading pages, inject, re-render). Never leave dotted leaders trailing into empty space.
- **Font embedding is Chrome's call.** A variable webfont, or no font files at all, produces outlined Type3 text. Ship static font files when preflight, PDF/A, or text extraction matter.
- **Figure-internal collisions are not linted** — that is Phase 4b's visual auditor. `pagecheck.py` measures where blocks land on the page, not inside a figure's coordinates.
- **`theme.contentWidth` is not a layout value.** The text column comes from `@page` margins; `contentWidth` only fixes the figure coordinate system.

## Managed files — the drift rule

`build.py`, `lint.py`, `pagecheck.py`, `typography.py`, and `style.css` are **managed**: scaffolded from this skill and re-synced by `upgrade`. Never patch them in a project — the fix belongs in `.pdf-press.config.json`, or in the skill's `assets/pipeline/` so every project gets it. If a project needs pipeline behaviour the config cannot express, extend the config schema.

`upgrade`: diff the project's managed files against `assets/pipeline/`, show the diff, copy on approval, bump `pipelineVersion`, re-run Phase 4.

## Versioning and release hygiene

- `version.policy: "date"` makes every rebuild a different file and `--repro` meaningless.
- Cut a new dated release folder rather than rebuilding into a previous one.
- To supersede an edition, set `deprecated: true` on it — renaming a rendered PDF by hand means the next build silently restores the old name.
- Adding a colour to `allowExtraColors` to silence lint needs a reason in the commit message.

## References (loaded on-demand)

- `references/cookbook.md` — task-shaped recipes: screenshot showcase chapters, emoji status badges + legend, custom cover art / client co-branding, non-technical wording and table hygiene.
- `references/config-reference.md` — every config key, defaults, and the failure mode when wrong.
- `references/theme-and-figures.md` — theme tokens, cover styles, figure conventions, the exact lint rules and how to satisfy them.
- `references/content-doctrine.md` — chapter skeleton, status vocabulary, generating endpoint/payload tables, edition scoping, supplier annexes (FDS / point list / FAT-SAT).
- `references/linking.md` — internal PDF jump links, external deep links into a live docs/API reference, resolving a renderer's anchor grammar, verifying targets resolve.
- `references/determinism.md` — what byte-identity depends on; ordered debug list when `--repro` fails.
- `references/migrating-an-existing-pipeline.md` — folding a hand-rolled PDF build onto this one without losing its identity.
- `assets/examples/example.pdf-press.config.json` — worked config: hero-fill cover, brand motif, two editions, two languages, variable webfonts.

## Cost note

Setup on a fresh project: ~3–6 min (venv install ~40s dominates). Rebuild of one target: ~5–15s.
