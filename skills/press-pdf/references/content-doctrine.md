# Content doctrine — what makes a specification contractual

Two failure modes ruin a signed architecture PDF: promising behaviour the system does not have, and restating the code from memory so tables drift from the contract. The doctrine makes both mechanically impossible; `lint.py` enforces what can be enforced.

## The status vocabulary

Every block making a normative claim carries `data-status`:

| Status | Means | Also requires |
|---|---|---|
| `IMPLEMENTED` | The deployed code does this today | `data-source="path/to/file.go"` (optionally `:line`) |
| `PLANNED` | Committed, not built. Named workstream or ticket in the prose | — |
| `PROPOSED` | Under discussion. Not a commitment | — |

```html
<p data-status="IMPLEMENTED" data-source="internal/commands/service.go">
  Každý příkaz musí být potvrzen do 30 s, jinak expiruje.</p>

<p data-status="PLANNED">
  Při konfliktu dvou pokynů se stejnou prioritou musí zvítězit novější.</p>
```

Lint fails on a normative marker (`musí`, `nesmí`, `must`, `shall`, …) in a block with no `data-status`, and on `IMPLEMENTED` without `data-source`. The badge renders on paper — a reader can never mistake a plan for as-built.

Soften or tag: background prose should not use a normative verb. "Platforma podporuje …" needs no tag; "Platforma musí …" does.

## Chapter skeleton

arc42-shaped. Adapt the numbering, keep the order.

| # | Chapter | Carries |
|---|---|---|
| 1 | Purpose & scope | Audience, edition scope, the status legend, what this document is **not** |
| 2 | System context | The boundary figure: actors, external counterparties, what crosses the line |
| 3 | Building blocks | Module/component map at the depth this edition allows |
| 4 | Runtime scenarios | Per scenario: trigger, interface, payload, expected response, error path |
| 5 | Interfaces | Endpoint and channel reference, generated from the contract artifacts |
| 6 | Command prioritization | The arbitration hierarchy, and what happens on a tie |
| 7 | Emergency handling | Comm loss, watchdog, safe state, recovery and catch-up |
| 8 | Cross-cutting | Security, identity, PKI, observability — as far as the audience needs |
| 9 | Deployment | Internal editions only, normally |
| 10 | Decisions | The choices a reader would otherwise re-litigate, and why |
| 11 | Open items | Owner + due date per row. An empty table is a lie; an honest one builds trust |

## One master, N audience renderings

When two editions are the same document for different readers, do not author two files. Author the honest badged master and derive the other (`editions.<name>.content`, see `config-reference.md`): the external edition renders the master with `data-status` stripped and `internal-only` sections dropped. Tag every normative claim in the master, put gap analysis in an `internal-only` section.

Two rules that bite:

- **`internal-only` must be a top-level `<section>`.** A `<div class="internal-only">` nested inside a chapter is *not* dropped — it ships to the external reader. An internal aside becomes its own section.
- **Read the derived PDF, not the master.** Everything the transformation removes is invisible in the source you were editing.

## Interfaces: generate, never retype

Endpoint and payload tables come **from the machine-readable contract**:

- REST → **OpenAPI 3.1** (`openapi.json`)
- MQTT / AMQP command + telemetry channels → **AsyncAPI 3.0** (`asyncapi.yaml`). 3.0 is operations-first: explicit `action: send|receive`, a central Messages Object, `reply` for request-reply — right shape for a command/ACK loop. A real break from 2.x — do not copy 2.x patterns into a 3.0 file.
- Anything else → JSON Schema next to the code.

If a surface has no such artifact (prose-only MQTT description is the usual gap), **authoring it is part of the specification job**, not a follow-up.

Per scenario, the row set: trigger · interface · request payload · success response · failure response · timeout/expiry · idempotency · status tag.

### Payload examples: canonical JSON, mechanically produced

Every JSON `<pre>` example ships in **canonical 2-space form** — the exact output of `json.dumps(obj, ensure_ascii=False, indent=2)`. Never hand-compacted or hand-wrapped: partners paste these into a client, and a line break inside a string makes them invalid.

Produce by round-trip, never by typing: parse the example, re-emit, escape back into the fragment. Anything that does not parse (state diagram, side-by-side request/response, deliberate elision) is left alone. `lint.py` warns on any `<pre>` whose JSON does not equal its canonical form.

Canonical form is taller — re-run `pagecheck.py` after a reformatting pass; a grown example changes where every following chapter breaks.

### Verify the artifact before you generate from it

A spec file in the repo is not automatically true. Diff it against the code that serves it — the router's route table, the publisher's channel list, the exported catalog — *before* generating a table; a stale OpenAPI file produces a confident table of endpoints that 404. Bringing the artifact up to date is part of this job; do it in its own commit so spec change and doc change stay separable.

### The generate-and-splice mechanism

Keep a small generator (`build/gen_tables.py` + a language-native dumper when the vocabulary lives in code, e.g. a `//go:build ignore` program printing the command catalog as JSON) writing HTML fragments into `build/generated/`, spliced between explicit markers:

```html
<!-- GEN:rest.cs.html — generated; do not edit by hand -->
<table class="breakable">…</table>
<!-- GEN-END -->
```

Re-running is idempotent and machine-derived tables are visible at a glance. Two refinements:

- **Generate the drift-prone columns, curate the prose column.** Paths, methods, verb names, wire types, parameter bounds and enums come from the artifact; the "what it's for" column is authored (in Czech for a Czech document — English `summary` strings read as an untranslated leak).
- **Assert, don't silently skip.** If the generator curates a row per endpoint, have it fail when a path/method it names is absent from the artifact — otherwise a renamed route quietly drops out and the omission ships.

## Command prioritization

Do not invent a priority hierarchy. Cite the standard the domain already has, then state where your system deviates and why.

For energy / DER dispatch: IEEE 2030.5 / CSIP model — `DERProgram → DERControl → DefaultDERControl`, where a higher-priority or newer event overrides an older one and `DefaultDERControl` is the fallback when no event is active. Layer **EN 50549-1** for curtailment limits, **IEC 61850-7-420** for DER logical-node vocabulary, **OpenADR 3.0** if the counterparty speaks it.

> **Verify before citing contractually.** The 2030.5 hierarchy above comes from the SunSpec CSIP implementation guide, not the paywalled IEEE text. A signed specification must quote the standard itself — check the clause number in the real document before it goes in a contractual annex.

State explicitly, with status tags: what wins on a tie, whether a lower-priority instruction is queued or rejected, how a rejection is reported, how long each level remains in force.

## Emergency handling

The three questions a controller supplier asks, in order:

1. **What is the safe state**, per device class, and who defines it — cloud or site?
2. **What triggers it** — watchdog interval, missed-heartbeat count, explicit stand-down?
3. **What happens on recovery** — do expired instructions replay, get re-pushed in bulk, or stay dead until the next planning cycle?

Answer each with a status tag. A comm-loss contract still awaiting vendor sign-off is marked `PROPOSED`, with the sign-off in Open items with an owner.

## Edition scoping

`editions.<name>.figures` is the mechanical gate; the prose gate is your judgement. Before widening an edition's scope, ask what the audience gains and what they could do with it that you would not want. Internal module maps, deployment topology, data models, and infrastructure names are the usual things an external edition must not carry.

Write `scopeNote` in the config when you make the call, so the next author does not quietly re-add what you deliberately removed.

## Writing technical prose in an inflected language

**1. Typography.** `typography.py` runs over text nodes at assembly time, never inside `<pre>`, `<code>` or `<svg>`. Configure per language under `content.typography`; Czech and Slovak get the full ČSN 01 6910 set by default:

| Rule | Stops |
|---|---|
| `prepositions` | a one-letter preposition/conjunction (`k s v z o u i a`) ending a line — the most common Czech typographic error |
| `beforeCode` | a short word stranded before an inline identifier (`povel s` ⏎ `externalKey`) |
| `units` | a value torn off its unit (`0,5` ⏎ `kW`), including inside slash-lists (`< 5 s/< 10 s`) |
| `operators` | `< ≤ ≥` splitting from their number, and `≤1 s` / `< 2 s` mixing on one page |
| `dashes` | an em dash opening a line |
| `references` | `kap. 7`, `§ 4.11`, `obr. 3` breaking after the abbreviation |

Never hand-type `&nbsp;` in content. If a rule misfires, fix the rule — it is a managed file and every project benefits.

**2. Terminology — decide the direction, then enforce it.**

Default for an engineering specification: **keep technical terms in English**. The reader types `SETPOINT_GRID`, searches `payload` in an AsyncAPI file, discusses `hard-check` with a vendor; Czech coinages differ per author and make the document contradict itself. **Do not "fix" English technical vocabulary in a Czech document unless the project has explicitly decided to.** The real risks are the typography around the term (rule 1) and inconsistency — the same concept written three ways.

`content.glossary` enforces whichever direction the project chose — a term → preferred-term map checked in prose:

```json
{"term": "vstupní kontrola", "prefer": "hard-check", "langs": ["cs"]}   // keep English
{"term": "batch",            "prefer": "dávka",      "langs": ["cs"]}   // localise
{"term": "setpoint",         "allow": true}                             // accepted, inventoried
```

Two invariants regardless of direction: lint reads **prose only** — identifiers inside `<code>`/`<pre>` are never candidates — and a rule scoped `langs: ["cs"]` must not police the English edition. A bare enum in running text (`typologie CONTROLLABLE`) belongs in `<code>`: marking a value as a value, not a translation decision.

**3. Status badges are code statuses.** `IMPLEMENTED` / `PLANNED` / `PROPOSED` stay English by default — a reviewer grepping `data-status="IMPLEMENTED"` should see the same word on the page. `content.doctrine.statusLabels` exists for a project that deliberately wants them localised; leaving it unset is right for a technical specification. For an executive/non-technical audience, single-emoji labels (✅ / ⏳ / ❓) render bare — no chip — with a chapter-1 legend table; recipe and caveats in `cookbook.md`, which also covers plain-language table wording and screenshot showcase chapters.

## Pagination — how the text falls on paper

`pagecheck.py` measures this; the rules below prevent most of it.

**What the stylesheet already guarantees** (do not re-implement per project): headings and kickers never end a page (`page-break-after: avoid`), figures and captions stay together, list items and code blocks do not split, `orphans`/`widows` are 3, table rows are atomic, and a `table.breakable` repeats its `thead` on every page it spans.

**What the stylesheet cannot fix — the chapter-tail problem.** Chapters start on a new page (`section.chapter { page-break-before: always }`), so a chapter's last section that does not fit lands alone on a near-empty page. This is a content decision:

- A stranded **cross-reference** ("the comm-loss scenario is specified in chapter 7") belongs in the chapter's lead, not its own section — moving it up removes the page.
- **Real content**: tighten the chapter above it until it fits, or shorten a preceding block so it starts earlier.
- **Never delete meaning-bearing text to win a page.** A short page is a blemish; a missing requirement is a defect. Reflow, don't amputate.

Expect ~40–60% fill on the last page of a chapter — that is the design working. Below ~25% fill, look.

**Check after every content edit, not once at the end.** Pagination is global: adding two lines to chapter 2 can strand chapter 4's tail. `pagecheck.py -v` after a build tells you which page moved.

## Supplier annexes — a different artifact class

For a hardware or controller supplier, an architecture PDF is **not** what gets signed. The industrial convention:

- **Functional Design Specification (FDS)** — what the controller shall do, per function;
- **I/O signal / point list** — every signal, address, unit, scale, range, direction;
- **FAT / SAT protocol** — the tests the supplier performs before and during integration, each with a pass/fail criterion and a place to record the result.

"Tests the supplier must run before end-to-end integration" is the FAT protocol. Author it in that shape — numbered test table with preconditions, steps, expected result, signature block — not as more architecture prose. Seed it from whatever simulator or scenario harness the repo already has: a test that runs in CI is a test the supplier can be asked to reproduce.

Keep the annex in the same document set (its own edition, or an appendix chapter of the external edition) so it inherits the theme, status vocabulary, and version stamp.

## Sign-off

LLM-drafted contract text hallucinates conditions not in the source, worse the longer the document. Two cheap countermeasures:

- **Receipts.** `data-source` on every `IMPLEMENTED` claim lets any reviewer check a sentence against a file in seconds.
- **Named human sign-off per chapter** in a regulated or contractual document — reviewer's name and date in the chapter's Open items or a sign-off table. Nothing in this pipeline substitutes for a person who read it.
