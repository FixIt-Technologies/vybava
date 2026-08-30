# Theme tokens & figure conventions

The theme half is generated, the figure half is enforced.

## How the CSS is assembled

```
.pdf-press.config.json  ──build.py:gen_theme_css()──>  theme.css   (@font-face, :root, @page, cover treatment)
                                               style.css   (structural, managed, zero brand values)
                                                    └─> both inlined into the render HTML
```

`style.css` may not contain a single hex code, font name, or page dimension — everything arrives as a custom property. A rebrand is a config edit.

### Tokens the stylesheet consumes

Colours become `--<key>` from `theme.colors`; signals become `--<name>` plus `--<name>-wash` from `theme.signals`. Fonts become `--font-body`, `--font-display`, `--font-mono` (each `'Family', stack`). The type scale becomes `--size-<key>`. `theme.cover.heightMm` becomes `--cover-height`.

Optional tokens degrade gracefully: `var(--brand-ink, var(--ink))`, `var(--pv, var(--dim))` — a minimal palette still renders.

### Cover styles

| `cover.style` | Treatment | Use when |
|---|---|---|
| `hero-fill` | Full-bleed brand background, diagonal motif streaks (vector SVG, injected at assembly — CSS masks rasterize in print), ink type | The brand owns a strong flat colour and the document is a showpiece |
| `panel` | Paper cover with a thick brand rule at the top | Default. Sober, prints cheaply, works with any palette |
| `minimal` | Type only | The document travels inside another cover, or the brand is type-first |

`cover.motif` (e.g. `///`) is injected via `::before` on `.kicker` and `.edition-tag`. Empty → both fall back cleanly, no orphan glyphs.

### Status badges

`[data-status]` renders an inline badge from the attribute value, coloured from the signal palette: `IMPLEMENTED` green, `PLANNED` amber, `PROPOSED` neutral. Suppressed on `figure`, `table`, and `section` so a wrapper can carry the status for lint without printing a badge. Put the attribute on the `<span>` or `<p>` to badge, or on a table cell's inner `<span>` for a status column.

## Figure conventions

Hand-drawn SVG, one file per figure, `NN-slug.svg`, inlined at build time.

| Rule | Value | Notes |
|---|---|---|
| `viewBox` width | `theme.contentWidth` (default 760) | Figure scales to 100% of the content column; different widths → different effective stroke weights and label sizes |
| Height | Whatever the drawing needs | Free |
| Box corner radius | `figures.lint.rx` (default 4) | Enforced on rects with width ≥ 40; narrower rects are chips and swatches. A **fully-rounded pill** (`rx ≥ height/2` — a state badge) is exempt |
| Box stroke | `figures.lint.strokeWidth` (default 1.2) | Other weights warn — thin connector lines at 0.6/0.8/1.0 are accepted |
| `font-size` | Only `figures.lint.fontSizes` (default 11 / 9.5 / 8) | Group title / body label / annotation |
| Colours | Only `theme.colors` + `theme.signals` (+ `#ffffff`, `#000000`) | Anything else is an error. Genuine exceptions go in `allowExtraColors` **with a reason in the commit message** |
| Text | Inherits `--font-mono` via `svg text` in `style.css` | `font-family="…"` only for a prose annotation in the display face |
| Language-specific strings | `{{t:key}}` from `labels.json`, every configured language | Technical literals — verbs, table names, routing keys, HTTP methods — stay inline. Do not translate `POST /v1/plans` |
| Edges | Solid = request/response or data flow; dashed = async/eventing | Shared `<marker>` defs, one neutral and one live-coloured |
| Partner variants | Suffix `-partner` | A redacted twin, not a second style |

### The fit factor — authored size is not printed size

Printed size = authored size × `column_width / theme.contentWidth`. On A4 with 16 mm side margins and the figure's 4 mm padding, the factor is **≈0.634**:

| Authored | On paper (A4, contentWidth 760) |
|---|---|
| 11 | 6.97 pt |
| 9.5 | 6.02 pt |
| 8 | **5.07 pt** |

`lint.py` warns when any configured size falls under `figures.lint.minRenderedPt` (default **5.0** — the practical floor for the annotation tier). Raise to **6.0** when the document has an accessibility requirement, will be printed below A4, or will be read on paper by people who did not draw it. To clear a higher floor: lower `contentWidth` (640 puts the 8 tier at 6.0 pt) or raise the sizes — either way apply it to **every** figure, so all figures share one factor.

### Font embedding — what Chrome will and will not embed

Chrome outlines any face it cannot embed into **Type3 fonts**: the page looks perfect and carries no real font. Two triggers:

- a **variable** webfont (`format('woff2-variations')`) — Skia cannot embed a variable instance;
- **no font files at all** — system faces are not embeddable.

Text still renders and extracts, but print preflight and PDF/A reject it, and file size grows. `build.py` reads its own output back and warns. For a signed or professionally printed deliverable, ship **static** font files (a fixed-weight `.woff2`/`.ttf` per weight) rather than one variable file.

No fallback at all in the footer stamp: reportlab draws it from `theme.footer.fontFile` and blanks a missing glyph. `lint.py` opens that TTF and checks every character of every `footerLeft` and `pageWord` against its cmap.

### Auto-numbering

Chapter, section, and figure numbers come from CSS counters, not typed text:

```html
<p class="kicker">{{t:chapter.word}} <span class="no"></span></p>
<h2><span class="no"></span> Chapter title</h2>
<h3><span class="no"></span> Section title</h3>
<figcaption><span class="fig-no">{{t:fig.word}} <span class="fig-n"></span></span> — caption.</figcaption>
```

Only an **empty** span is filled, so content with literal numbers is untouched — migration is incremental. The language-specific words ("Kapitola" / "Chapter", "Obr." / "Fig.") stay `{{t:…}}` labels because CSS cannot know the language.

### Signals carry meaning

Assign each signal name once, project-wide; never reuse a colour for a second meaning. Default (energy-domain) assignment:

| Signal | Colour | Means |
|---|---|---|
| `pv` | amber | photovoltaic / generation |
| `bat` | green | battery / storage |
| `grid` | blue | grid / network connection |
| `backup` | violet | backup / islanded operation |
| `alarm` | red | fault, alarm, degraded |
| `live` | green | live data path |

For a non-energy project, rename the signals to that domain's concepts.

### A figure that passes lint

`assets/templates/figures/01-system-context.svg` is the reference: the `viewBox` contract, both marker defs, solid and dashed edges, all three font sizes, palette-only fills, `{{t:key}}` labels for every human-readable string. Copy it, redraw it, run `lint.py` — do not start from a blank file.

### Labels

`figures/labels.json` is a flat `key: { lang: string }` map shared by figures and content. Namespace keys by figure (`fig3.ingest.decode`) so an unused key is obvious when a figure is redrawn. Lint reports missing languages as errors and unused keys as warnings — clean up warnings when a figure is retired.

## Debugging a figure that looks wrong on paper

- **Labels overflow their box.** Czech is ~10–20% longer than English. Size boxes for the longest language; check both PDFs. `lint.py` estimates mono label widths after `{{t:key}}` resolution and warns on a label crossing its box, a neighbouring box, or the viewBox — heuristic: a warning means "go look", not noise. Custom cover art and co-branding recipes: `cookbook.md`.
- **Strokes look heavier than the neighbouring figure.** The `viewBox` width is wrong. Lint catches this — you skipped it.
- **Text renders in a fallback face.** The mono family lacks a glyph (usually a Czech diacritic). Check the `unicode-range` split on the `@font-face` entries: latin-ext file first, both listed.
- **The figure splits across a page break.** `figure` is `page-break-inside: avoid`; a figure taller than the text column cannot be kept whole. Split the drawing into two figures — never shrink below the readable label size.

## Orientation — the axis a timeline or comparison runs along

Figures are drawn at a fixed `viewBox` width inside a portrait text column: **horizontal is scarce, vertical is free**. Any figure whose content grows with item count — latency timeline, stage pipeline, comparison of alternatives, checkpoint sequence — goes on a **vertical axis**, items stacked, labels beside them.

A horizontal timeline puts each caption under its tick; captions longer than the tick gap (the first real sentence, always in the longer language) collide unreadably and push past the page width. Nothing in the pipeline sees inside a figure — only the Phase 4b visual auditor or a human catches it.

Rules of thumb:

- **Stacked axis, labels to the side** — every item gets a full line of horizontal room for its title and note.
- **Values on one side, names on the other** — measurement left, meaning right (or the reverse, consistently). The reader scans one column, not a zigzag.
- **Boxes "outside the contract" stay full-width, top and bottom** — upstream/downstream actors read as before/after and free the middle band.
- **Height is not a cost until it exceeds the text column.** Then split the figure — never scale it down: the label size is the readability floor and every other figure's strokes are calibrated to the same width.
- **A wide table is the same problem** — a many-column comparison is a broken page in print; reshape as rows.

Verify by rendering the page to PNG and *reading* it, in the longest language. "It fits in the SVG viewBox" is not the check; "no two texts touch and nothing crosses the frame" is.
