# Cookbook — task-shaped recipes

Recipes learned on real documents (first: the EVE × ČSOB client spec, 2026-08); when a recipe hardens into a rule, it graduates into the pipeline or a topical reference and leaves a pointer here.

## Embed product screenshots as a showcase chapter

1. **Capture** at a wide viewport (~1680px), device-scale screenshot, then downscale to ~2000px wide (`sips -Z 2000`). Store under `<project>/shots/`.
2. **Paths**: the render HTML is written into the project root, so `<img src="shots/x.png">` resolves relative to the config's directory.
3. **Keep each exhibit whole**: wrap `h3 + intro paragraph + figure` in `<div style="page-break-inside: avoid;">…</div>` — otherwise the caption (or whole figure) strands on the next page and pagecheck fires "page opens with a figure caption".
4. **Frame it**: `<img style="width: 100%; border: 0.3mm solid var(--edge-strong); border-radius: 2mm;">` — vars only, no literal colors.
5. **Confidentiality sweep — always.** Real UI shows real data: other clients' names, repo slugs, monetary amounts, e-mail addresses. Read every screenshot before it ships and either accept each disclosure deliberately (social proof) or blur/crop. The pipeline cannot make this judgement.
6. Size check: a 2000px PNG of dark UI ≈ 0.5–0.9 MB; six exhibits keep the PDF emailable. JPEG smells hand-made on UI text — stay PNG.

## Emoji status badges + legend (non-technical audience)

Word badges (`IMPLEMENTED`) read as jargon to executives. Swap the printed label via config — the `data-status`/`data-source` discipline underneath is untouched:

```json
"content": { "doctrine": { "statusLabels": {
  "IMPLEMENTED": { "cs": "✅" }, "PLANNED": { "cs": "⏳" }, "PROPOSED": { "cs": "❓" }
}}}
```

- The theme generator renders a **single pictographic glyph bare** — no chip border, no wash, `0.8em` — automatically (word labels keep the chip).
- **Match the glyph to the meaning.** ❌ reads as "rejected"; for "open for discussion" use ❓ — or reword the legend so the symbol is honest.
- **Legend as a chapter-1 table**: one row per status, first cell an empty `<span data-status="…">` (the badge renders itself), second cell the plain-language meaning. `IMPLEMENTED` still needs `data-source` — point it at the repo README.
- Glyph availability: subsetted webfonts rarely carry emoji; Chrome falls back to the system color-emoji face, which prints fine but embeds as bitmap glyphs — irrelevant for e-mailed documents, disqualifying for PDF/A.

## Custom cover art / client co-branding

The generated hero-fill cover injects its streaks as vector SVG (pipeline ≥ v4). To go further — a brand gradient, a client logo — own the cover in content:

- **Full custom art**: make the first child of `.cover` an `<svg class="cover-art" viewBox="0 0 210 296" preserveAspectRatio="none">` with your gradient/shapes. The `cover-art` class both positions it (managed CSS) and tells the pipeline to skip its own streak injection. Gradient stops via `style="stop-color: var(--brand)"` — vars, not hexes; register any new brand color in `theme.colors` first.
- **Never draw print art with CSS `mask-image` or `filter`** — Chrome's print pipeline rasterizes masked/filtered layers at low DPI. Vector SVG shapes stay crisp. Verify the shipped page carries **zero raster images** (walk the page's XObjects).
- **Client logo on a colored cover**: inline the client's SVG in `.cover-head` inside a white chip — `<div class="logo" style="background: var(--paper); border-radius: 2.5mm; padding: 2.5mm 3mm;">` — so its own brand colors survive on any background. Client brand art keeps its literal colors; that is the one sanctioned hex exception.

## Wording & tables for a non-technical reader

- **Plain-language row headers.** "Odchozí provoz" became "Komunikace do internetu" — if a header needs the note to explain itself, rename the header.
- **Never presume the client's internal processes.** "zapadá do vašich stávajících záloh" claims knowledge you don't have; "napojení na vaše zálohovací postupy doladíme s vaším IT" says the same thing honestly.
- **A pilot limit is not a technology limit** — when stating a deliberately small number (concurrency, scope), say which it is, and back the capability claim with an `IMPLEMENTED` source.
- **Label columns don't wrap**: `style="white-space: nowrap;"` on first-column `<td>`s ("Rozsah pilotu", "Měsíce 2–3") — a two-line label reads as a typo.
- Internal cross-references become jump links — see `linking.md`; verify the `/Link` annotations and named destinations in the shipped PDF, not the HTML.
