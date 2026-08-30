---
name: press-offer
description: "Czech commercial offer documents (cenová nabídka, funkční specifikace, dopady) as editable DOCX in the issuer's house style. Press skill family."
disable-model-invocation: true
---

# press-offer — Czech commercial offer documents (docx)

Produce a client-ready Czech commercial docx (offer / functional spec / impacts doc) in the issuer's established house style. Sibling of `press-pdf`: same family law, DOCX where the client needs an editable Word deliverable, PDF otherwise. The skill is the offer STRUCTURE (anatomy, pricing conventions, register) — not generic docx authoring.

Every issuer-specific value — company name, IČO, DIČ, registered address, data box, day rate, brand colours — comes from the machine-local identity file, never from this skill. Run `press identity init` once per machine, fill it in, and `press identity show --json` feeds the generator.

## Press family (READ FIRST)

Part of the **press skill family** with `press-pdf` and `press-logo`. Shared law: `press doctrine` (`press doctrine --schema` for the config schema). State goes through the `press` CLI — never hand-edit `.press.conf.json` / PRESS.md's autogen block.

- `press resolve` → project name; outside git the CLI refuses — ask the user, then pass `--project`. `press init` (idempotent) ensures `~/Exports/<project>/` exists.
- Client identity: given an IČO, `press ares <IČO>` — name, address, DIČ from the ARES registry, never retyped.
- Issuer identity + house style: `press identity show --json`. If it reports missing fields, STOP and tell the user to fill them in at `press identity path` — never invent or substitute an issuer's registry details.
- Home: `~/Exports/<project>/docx/<type>/<slug>/` (mirrors press-pdf's `pdf/<type>/<slug>/`) — generator script, assets, and the rendered docx all live there.

**Reference implementation:** `template.mjs` in this skill directory — every helper, already wired to the identity file.

## Workflow

1. **Gather inputs via batched multiple-choice rounds** (AskUserQuestion, ≤4 at a time, recommendation-first options with tradeoffs). Only real forks get asked — facts you can look up (ARES identity, source docs, prior offers in `~/Exports`) are your job. Settle at minimum: document type (nabídka / specifikace / dopady), subject + scope, delivery variants and which to recommend, pricing inputs (MD estimates per item, monthly provoz/podpora), milestones/billing split. If the user has a source doc, read it first — never invent scope.
2. **Copy `template.mjs` into the document home**, `npm init -y && npm install docx@8`, fill the CONTENT section, run `node gen_<name>.mjs`. The generator resolves its own directory — never call `press resolve` from there, the document home is outside git by design.
3. **Output** lands beside the script; set `PRESS_OUT=<Type>_<Subject>_<YYYY-MM-DD>.docx` to name it. Filename ASCII, underscores.
4. **Verify before handing over:** unzip the docx, strip tags from `word/document.xml`, assert key figures/names present, count `word/media` images if any were embedded. Never deliver unverified.
5. Report the absolute path + a compact pricing/structure summary — always flag that MD estimates and prices are a draft for their sanity-check.

## House style (resolved from the identity file — do not restyle per document)

Colours (`brand.accent`, `tableHead`, `zebra`, `hairline`, `text`, `muted`) and `brand.font` all come from `press identity`. Fixed typography: body sz 21 (10.5 pt), tables sz 19, captions italic 18. A4, 1134-twip margins. Header = bold accent issuer name + right-tabbed doc label above an accent rule; footer = issuer line (name · IČO · address) + "Strana X / Y" under an accent rule. Chapters as `N · Název` in accent sz 30; subsections bold sz 24. `**bold**` mini-markdown works in every helper.

## Document anatomy

**Cenová nabídka** (compact, ~4–6 pages):
1. Title block: `Cenová nabídka` / bold subject line / muted tagline describing the issuer's field
2. Identity table Dodavatel/Objednatel — the Dodavatel column is filled from `press identity`, the Objednatel column from `press ares <IČO>`; then Datum vystavení + Platnost nabídky do (`commercial.validityWeeks` out, default ~6 weeks)
3. `1 · Předmět nabídky` — what + reference to the spec/annex; one paragraph on why this delivery is real (authorship, existing platform, known risks)
4. `2 · Rozsah dodávky a varianty` — variants as h2 + bullets; mark the recommended one `(doporučeno)`
5. `3 · Cena` — rate sentence first, built by `rateSentence()` from `commercial.dayRate`/`currency`/`rateUnit`/`vatNote`; then 3.1 jednorázové položky (columns Položka / Rozsah in MD / Cena, right-aligned prices), 3.2 provoz a podpora měsíčně. Monthly ops+support runs 12 months with auto-prolongation, 3-month notice.
6. `4 · Milníky a akceptace` — M1..Mn table, terms relative to M1; 14-day acceptance; billing split per milestone (e.g. 40/30/30)
7. `5 · Předpoklady a vymezení` — bullets; ALWAYS include the fallback clause `Při porušení předpokladu přechází dotčená položka na sazbu <rate>.` (same `rateSentence()` figures) and explicit out-of-scope items
8. `6 · Proč <dodavatel>` — 3 bullets max: authorship/zero ramp-up, proven reference platform, contractually clear scope

**Funkční specifikace** (annex-grade):
1. Title block + purpose chapter (explicitly: usable as **příloha vymezení plnění**)
2. `2 · Pojmy a kontext` — terms table + one tech-stack paragraph
3. `3 · Současný stav` — one h2 per screen, each: prose of what the screen does (bold the key nouns), then `img(path, 'Obr. N — caption')`. Screenshots 1440×900, embedded at width 620. End with `Známá omezení současného stavu` bullets.
4. `4 · Cílová funkčnost` — h2 per capability with bullets; close with `Mimo rozsah` bullets
5. Annex chapters as `A · Příloha — …` with protocol/table detail

## Wording register

- Professional Czech business prose, **no marketing fluff**, no anglicisms where a Czech term exists (povel/příkaz, zařízení, vymezení plnění, součinnost, kvitace).
- Bold the load-bearing phrase of each paragraph, not whole sentences.
- Sell with facts, not adjectives: authorship, existing running code, known firmware quirks, "bez skrytých poplatků", "termínově závazné".
- Every price table row carries its scope in MD so the price is auditable; never a bare lump sum.
- Assumptions written as protections, not disclaimers — each names the consequence (rate fallback).
- Numbers: non-breaking-space thousands (`1 250 000 Kč`), `bez DPH` stated once at the top of the price chapter, MD = člověkoden.

## Pricing conventions

- Rate anchor: `commercial.dayRate` from `press identity` — the issuer's established rate. Change it only if the user says so, and never inline a different number into one document.
- Price fairly at real hours — never undercharge to be nice.
- Structure: small fixed items (spec ~0,5 MD), delivery variants A/B as MD × rate, monthly provoz (hosting/backups/monitoring) + podpora (NBD response, a couple of hours included), overflow billed at the MD rate.
- Reference baselines: prior offers recorded in `~/Exports/<project>/` via `press index list --kind pdf`. Read the closest comparable before pricing a new one — never anchor on memory.

## Gotchas

- docx-js sizes are half-points (`size: 21` = 10.5 pt); margins/tabs in twips (1134 = 2 cm; right tab 9640).
- `ImageRun` needs explicit pixel `transformation` — compute height from the real aspect ratio (`sips -g pixelWidth -g pixelHeight`).
- Page numbers: `PageNumber.CURRENT`/`TOTAL_PAGES` as `children` of a TextRun, not `text`.
- Editing an EXISTING generated docx means surgical OOXML edits (the `docx` document skill), not a re-render; this skill is for NEW documents only.
- Scratchpad is ephemeral — later iteration re-runs this skill; the durable artifacts are the docx and this template.
