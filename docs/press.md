# press — the document family

`press` is the determinism layer behind four skills that produce client-facing
documents:

| Package | Kind | Produces |
|---|---|---|
| `press` | applet | project resolution, `~/Exports/<project>/` state, ARES lookups, the family doctrine |
| `press-pdf` | skill | offer, documentation, and legal PDFs — themed, linted, byte-reproducible |
| `press-logo` | skill | logo, brand mark, favicon, and the brand package around them |
| `press-offer` | skill | Czech commercial offers and functional specifications as editable DOCX |
| `press-email` | skill | client and prospect emails as Outlook-paste HTML, driven by a per-project `emails/FACTS.md` |

Install the whole set:

```sh
vybava install press-family
```

## Why a CLI sits under the skills

Every mutation of a project's deliverable home — `.press.conf.json`, the
`PRESS.md` index, artifact notes — goes through `press`. The skills never
hand-edit that state. This is what makes the output reproducible: one writer,
sorted keys, an index regenerated from the config rather than typed.

Two properties the CLI guarantees and a prompt cannot:

- **Idempotent creation.** `press init` on an existing project changes nothing.
  Prose you wrote in `PRESS.md` outside the autogen markers survives every
  regeneration, and an artifact note is only ever seeded, never overwritten.
- **Identity that is looked up, not retyped.** `press ares <IČO>` resolves a
  Czech company from the public registry and caches it in the project config.

## Commands

```sh
press resolve                    # project name for this checkout (the git repo name)
press init                       # create ~/Exports/<project>/ + config + PRESS.md
press config get <dot.path>
press config set <dot.path> <value>
press index add --kind pdf --type offer --file offer/x.pdf --title "…" --status draft
press index list [--kind pdf|logo|design]
press ares <ICO>                 # company identity from the ARES registry
press lint [--fix]               # validate project state; exits 1 on findings
press doctrine [--schema]        # the family's shared law, or the config schema
press identity init|path|show    # the machine-local issuer identity
```

Output is human-readable by default; `--json` switches every command to its
stable machine shape, which is what a skill or CI should parse.

Every command also exists under the multicall binary — `vybava press lint` and
`press lint` are the same code. `--project <name>` overrides git-based
resolution and is required outside a repository; there is deliberately no
fallback, so a document can never be filed under a guessed project.

`~/Exports` is the deliverable home; `PRESS_EXPORTS` overrides it.

## Identity is machine-local, never committed

A commercial document carries the issuer's registry identity, day rate, and
brand. That is private business data and Výbava is a public repository, so none
of it lives here. It lives in one file outside any checkout:

```sh
press identity init     # scaffolds ~/.config/press/identity.json, mode 0600
press identity path     # where it is
press identity show     # what is in it, and which required fields are empty
```

`PRESS_IDENTITY` overrides the location. The file holds three blocks:

- `issuer` — name, IČO, DIČ, address, data box, email, web
- `commercial` — day rate, currency, rate unit, VAT note, offer validity
- `brand` — accent, table head, zebra, hairline, text, muted, font

`press-offer`'s generator reads it through `press identity show --json` and
refuses to render a document while a required field is empty, rather than
producing one with blanks in the header. This is also what makes the family
usable by someone who is not its author: fill in your own identity and the same
skills produce your house style.

## The doctrine ships inside the binary

`press doctrine` prints the family's shared law — directory structure, config
layout, index and memory syntax — and `press doctrine --schema` prints the JSON
Schema for `.press.conf.json`. Both are embedded in the binary.

They used to be files in a separate repository that all three skills referenced
by absolute path. That path rotted the moment the repository moved, and it
disappeared entirely when the repository was folded into Výbava. Embedding is
what keeps one source of truth reachable from wherever a skill happens to run.
