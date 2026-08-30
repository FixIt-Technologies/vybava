# Links — internal jumps and external deep links

Two kinds, different failure modes, different verification. A broken link looks exactly like a working one until clicked.

## 1. Internal jumps (inside the PDF)

Cross-references — open points (`O16`), decisions (`12 Δ`), chapters, annexes — become real jump targets: Chrome's print pipeline turns `<a href="#id">` into a PDF internal link when the `id` exists in the same rendered document.

**Anchor the rows, not the prose.** Put the id on the element the reader should land on (table row, section), and link every mention:

```html
<tr id="ref-o16"><td>O16</td><td>…</td></tr>
…
<p>… řešit spolu s <a href="#ref-o16">O16</a>, protože …</p>
```

Mechanics:

- **Anchor once, reference many.** A registry (open points, decisions, requirements) is the natural anchor home — one id per row, every prose mention links to it.
- **Scripted sweep, not by hand** — hand-wrapping 35 references guarantees misses. Regex row labels into ids, regex prose mentions into links, shield the label cells so the anchor's own text isn't wrapped. Make the sweep idempotent (skip anything already inside an `<a>`) — you will run it again after the next content edit.
- **Never invent an id scheme per chapter.** `ref-o16`, `ref-d12` — one prefix, lowercase, derived mechanically from the label.
- ⚠️ **Derived editions can orphan a link.** The external edition drops `internal-only` sections *after* the link was written, so a jump that resolves in the master can point at nothing in the shipped edition. `lint.py` checks anchors against the DERIVED text; if it fires, either the target belongs in the shared body or the reference should not be a link in that edition.

## 2. External deep links (into the docs portal / API reference)

The PDF is a snapshot; the online reference is the live truth. Link *into* it — per section, and per operation where the target supports it.

**Resolve the deep-link scheme from the running site, never from memory.** Renderers (Scalar, Redoc, Fumadocs, Swagger UI) each generate their own anchor grammar, and it changes between versions. Procedure:

1. Open the deployed page in a browser (`playwright`), authenticated the way the reader will be.
2. Enumerate the real ids: `[...document.querySelectorAll('[id]')].map(e => e.id)`, filter for the operation/section pattern.
3. Derive the rule from what you see (Scalar, verified 2026-08: `#api-1/tag/{tag}/{METHOD}{path}`).
4. **Prove the fragment scrolls**: navigate to `page#fragment`, wait for hydration, assert the target's `getBoundingClientRect().top` is near the viewport top. An anchor that exists is not one the app scrolls to — client-rendered pages often need their own routing to honour a load-time fragment.
5. Generate the links from the same machine-readable source the tables come from (`openapi.json` tags + paths), so a renamed operation moves the link automatically. Rows the source cannot resolve fall back to the page-level link — never guess a fragment.

**Role-gated portals**: link only to paths the document's audience can open. Verify with the audience's own account, not yours — a partner PDF linking into an internal tree hands the reader a 403.

**Link-target hygiene** (lint enforces the shape; you enforce the reachability): absolute `https://` only, no ellipsis or space, no dev/local host, and one canonical host — if `docs.x.cz` 301s to `x.cz/docs`, publish the destination.

## Verification — the part that is actually skipped

Lint is offline, so it only judges shape. Reachability is a live check, done **before the PDF is issued**, with the reader's credentials:

```bash
# every distinct external target in the master content, as the AUDIENCE sees it
grep -o 'href="https://[^"]*"' build/content/<edition>.<lang>.html | sort -u
# → for each: curl -s -o /dev/null -w '%{http_code}' -b <audience-session> <url>
```

200 for every one, or the link does not ship. Two traps: a portal answering 200 to *you* (admin) and 403 to the partner, and a fragment that 200s on the page while scrolling nowhere — check both, once, at issue time.

Record what was verified in the release notes: "14 deep links, all 200 with the partner account, fragments confirmed scrolling". A future reader of the release folder cannot re-derive that.

## When the link target is a viewer you control

If the deep link lands in an embedded API viewer, the viewer is part of the deliverable: strip decoration that reads as noise in a contractual context (background flares, star fields, spotlight gradients) and remove affordances that cannot work — e.g. a "try it / open in client" button handing a session-gated spec URL to a third-party service, which then 404s. A dead button in the reference damages credibility as much as a dead link in the PDF.

⚠️ Viewer assets embedded in a server binary (`go:embed`) go live with the **next server deploy**, not with a docs sync — sequence the deploy before announcing the link is live.

## The verification that actually catches this

Both link kinds share one failure shape: **the source is right and the artifact is wrong.** Check them where they ship.

- **Internal jumps** — after the build, every `/Link` annotation's `/Dest` must resolve in the PDF's Catalog. `build.py` does this on every render and warns; if the warning appears, a post-processing step dropped `/Dests` (SKILL.md § The layer rule). Do not "fix" it by removing the links.
  ```python
  from pypdf import PdfReader
  r = PdfReader(path); defined = set(r.named_destinations)   # keys keep the leading slash
  dead = [str(a.get_object()["/Dest"]) for p in r.pages for a in (p.get("/Annots") or [])
          if a.get_object().get("/Subtype") == "/Link"
          and isinstance(a.get_object().get("/Dest"), str)
          and str(a.get_object()["/Dest"]) not in defined]
  ```
- **External deep links** — enumerate the `/URI` annotations out of the finished PDF (not the HTML) and resolve each as the audience, including the fragment check. Generate links only for targets present in the spec the **viewer serves**; a generator merging several specs will emit fragments that match nothing, landing the reader at the top of the page with no error.

Both checks take seconds and belong in the pre-issue routine, next to `pagecheck.py`.
