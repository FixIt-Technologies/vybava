---
name: press-logo
description: "Logo / brand mark / favicon / brand identity creation and iteration — decision-led brainstorm → SVG takes on a local preview website → standardized ~/Exports/<project>/logo/ package with DESIGN.md, rasters, press-CLI state."
---

# press-logo — brand marks, the press family way

Part of the **press skill family**. Shared law (directory structure, config, memory syntax): `press doctrine`. State mutations go through the `press` CLI — never hand-edit `.press.conf.json` or PRESS.md's autogen block.

## Phase 0 — Resolve the project (always)

1. `press resolve` — in a git repo prints the project name. Outside git it refuses: **ask the user** which project/company, then use `--project <name>` on every subsequent call.
2. `press init` — idempotent; creates `~/Exports/<project>/` + config + PRESS.md.
3. Read `~/Exports/<project>/logo/DESIGN.md` and `DECISIONS.md` if they exist — an existing brand means ITERATION (respect settled decisions), not reinvention.
4. Fill `project.type` / `project.description` via `press config set` if empty — one short question if the repo doesn't answer it.

## Phase 1 — Decision-led brainstorm

Run the decision-map flow (the `/brainstorming` doctrine, condensed for logos). Print the map as visible text, then batch questions with `AskUserQuestion` (previews with ASCII mockups for structural forks). The map:

1. **Core mark concept** — what the symbol IS. Mine the name for double reads (operators, ligatures, hidden glyphs) and the domain for dev-culture motifs (terminal, brackets, pixels, git graphs, code lines, circuits, keycaps, tags).
2. **Construction style** — solid geometry / monoline / faceted; affects favicon survival.
3. **Color system** — monochrome / accent / gradient / multi-token.
4. **Composition of the square** — tile (app-icon ready) / bare mark / mark+wordmark.
5. State geometry discipline as an assumption, don't ask: **512×512 integer grid, integer coordinates, 45°-only facets** where angular.

"Vastly different" takes requested → 10+ distinct concepts in one round — vary the IDEA per take, keep the settled system (palette, tile, grid) constant.

## Phase 2 — Draw, render, VERIFY, show on the preview website

1. **Draw** each take as a standalone SVG on the 512 grid (`viewBox="0 0 512 512"`, tile `rx≈112`, one `<linearGradient>` def per file). Complex repeated geometry (pixel grids) → generate the SVG with a short Python script, never hand-compute dozens of coordinates.
2. **Render + look at every take yourself** before showing the user: `qlmanage -t -s 512 -o <scratchpad> <files>.svg`, then Read the PNGs. Fix what doesn't read and re-render. Never hand over an unverified drawing.
3. **preview.html** in `~/Exports/<project>/logo/` — local comparison website, refreshed every round, opened with `open preview.html`:
   - grid of takes (`<img src="...svg">`, ~240px, rounded, soft shadow), caption = name + one-line concept;
   - winner/anchor pinned at top with an outline highlight as rounds progress;
   - a **favicon-size strip**: every take repeated at 32px — small-size legibility judged every round, not at the end;
   - checkered background behind transparent variants.
4. **Iterate in rounds**: user picks/redirects → next round builds on the pick (colorways, framings of the same idea). Log every round in `DECISIONS.md` as it happens.

## Phase 3 — Final package

Directory law (see CONVENTIONS.md): `exploration/` (every take ever drawn — history kept, not deleted), `final/` (master SVGs), `exports/` (rasters), `DESIGN.md`, `DECISIONS.md`, `preview.html`.

`final/` always ships six masters:
| File | Use |
|------|-----|
| `<name>-logo.svg` | primary tile (dark) |
| `<name>-logo-light.svg` | light tile |
| `<name>-mark.svg` / `<name>-mark-light.svg` | bare transparent marks for dark/light surfaces |
| `<name>-favicon.svg` | simplified cut that survives 16px (fewer, fatter elements) |
| `<name>-mono.svg` | single-color `currentColor` cut for print/embroidery |

`exports/` raster set: logo 1024/512/256/192, `apple-touch-icon-180.png`, light-512, transparent marks 1024, `favicon-16/32/48/64.png`, multi-size `favicon.ico`.

**Raster recipe (the only correct one):** headless Chrome → `"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless=new --disable-gpu --screenshot=<out> --window-size=512,512 --force-device-scale-factor=2 --default-background-color=00000000 file://<abs>.svg`, then Pillow LANCZOS downscales from the 1024 master; ICO via Pillow `save(..., sizes=[(16,16),(32,32),(48,48),(64,64)])`. ⚠️ NEVER ImageMagick's built-in SVG renderer — it breaks gradients and small sizes. Verify a corner pixel's alpha and one mark pixel's color after export.

`DESIGN.md` = brand source of truth: palette table (hex + role), file inventory, usage rules (which file when, clear space, minimum sizes, don'ts), exact geometry numbers, and the raster regeneration recipe.

## Phase 4 — Register state

1. `press index add --kind logo --file logo/final/<name>-logo.svg --title "<mark name>" --version <n> --status final` (one entry per shipped master set; iterate version on redesigns).
2. `press config set logo.mark "<one-line mark description>"` and `press config set logo.palette '<json of the core hex tokens>'`.
3. `press lint --fix` — leave the project clean.
4. Hand the user the paths + reopen `preview.html` one last time.
