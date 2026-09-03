---
name: press-email
description: "Client/prospect email as one Outlook-paste HTML under ~/Exports/<project>/emails/ — house markup, project FACTS.md, verified links, committed. Press skill family."
disable-model-invocation: true
---

# press-email — client email as Outlook-paste HTML

Draft `$ARGUMENTS` (recipient + purpose: offer, follow-up, hardware advice) as one HTML file the user opens in a browser, selects all, pastes into Outlook. Empty → ask recipient and what the call/thread settled.

## Press family (READ FIRST)

Part of the **press skill family** with `press-pdf`, `press-logo`, `press-offer`. Shared law: `press doctrine`.

- `press resolve` → project; outside git the CLI refuses — ask, then pass `--project`. `press init` (idempotent) ensures `~/Exports/<project>/`.
- Home: `~/Exports/<project>/emails/<YYYY-MM-DD>-<slug>-<lang>.html`; a multi-mail thread gets a `<client>/` subdir.
- `~/Exports/<project>/emails/FACTS.md` is the project's fact sheet: prices (+ the source file to re-verify them against), links, references, hardware per country, voice notes. Missing → seed it from what the user tells you before drafting; never invent a fact. Prices go in the email only after re-verifying against the source FACTS.md names; when two site sources disagree, the pricing page wins.
- Signature: sender = the user; `{{brand}}`, `{{web}}`, `{{tagline}}` from FACTS.md or `press identity show --json` (issuer web/email). Never retype registry identity into the email body.

## Output

- Add one line to the "Deliverables index" in `~/Exports/README.md`.
- Commit directly on `main` (`<Project>/emails: …`) and push. Never a worktree in `~/Exports`.
- Never send. Never change a price already quoted to a prospect without asking.

## Copy

- Language of the thread; Czech also for Slovak prospects.
- Voice: direct, concrete, honest about risk ("na rovinu", "bez záruky"), no marketing filler. Offer ≲ 400 words.
- Offer spine: pozdrav → co produkt dělá (demo + ceník links, 1–2 references inline) → hardware/technical verdict → ceník → co potřebujeme → podpis. Drop unneeded sections.
- Onboarding is done by the user: ask for fakturační údaje, never link self-serve onboarding.
- Recommend only hardware the product runs in production; unsupported platforms: say so and steer, no bespoke-implementation offer.

## Markup

Start from `template.html` (placeholders `{{…}}`); keep its system:

- Everything inline `style=`. No `<style>`, classes, images, flex/grid.
- Tables `role="presentation" cellpadding="0" cellspacing="0" border="0"` at `width:100%`. No outer wrapper, `max-width`, `align="center"` or `margin:auto`.
- Font `'Segoe UI',-apple-system,Arial,Helvetica,sans-serif`, 15px, line-height 1.6, text `#1f2933`.
- Palette (FACTS.md may override): rule `#06b6d4`; headings/links `#0e7490`; muted `#5b6b7a`; hairlines `#d7e3e8` / `#e8eff2`; tinted row `#fbfeff`; callout `#ecfeff` with 4px left rule `#06b6d4`.
- Eyebrows 12px / 700 / letter-spacing 1.2px / uppercase / heading colour.
- Every `<a>` carries `color` + `text-decoration:underline` inline.
- No `{{…}}` left in the output.

## Verify (before commit)

```
bun run ~/.claude/skills/press-email/scripts/check.mjs <file>
```

Self-installs Playwright into `~/.cache/press-email` on first run. Prints overflow at 640 / 390 px, screenshots to `/tmp/press-email/`, HTTP status + title of every link (alza.* 403s curl and WebFetch; only this check counts); exit 1 on any failure. Read both screenshots. Require: exit 0, `grep -c '<style\|<img\|max-width\|{{' <file>` = 0.

Hand back: absolute file path, commit link, and any fact you could not verify.
