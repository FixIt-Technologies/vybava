# Press family — conventions (v1.0.0)

The single source of truth for the `press-logo` and `press-pdf` skills and the `press`
CLI. The CLI is the ONLY writer of family state — skills never hand-edit
`.press.conf.json` or the autogen block of `PRESS.md`.

## Directory law

```
~/Exports/                        # the exports root (override: PRESS_EXPORTS)
└── <project>/                    # 1st level = project/company name
    ├── .press.conf.json          # family state (CLI-managed)
    ├── PRESS.md                  # Obsidian-syntax index (autogen block CLI-managed)
    ├── logo/                     # press-logo home
    │   ├── DESIGN.md             # palette + brand guidelines (source of truth)
    │   ├── DECISIONS.md          # brainstorm history
    │   ├── preview.html          # local comparison website
    │   ├── exploration/          # every explored SVG take
    │   ├── final/                # master SVGs
    │   └── exports/              # generated rasters (PNG/ICO)
    └── pdf/                      # press-pdf home (whole pipeline lives here)
        ├── offer/<slug>/         # one build dir per offer
        ├── documentation/<slug>/ # internal + external editions of the same sources
        └── legal/<slug>/         # documents meant for signature
```

Rules:
- **Project name** = git repo name of the cwd where the skill was invoked
  (`press resolve`). Outside a git repo the CLI refuses to guess — the skill ASKS the
  user and passes `--project`.
- **Creation is idempotent, never destructive.** `press init` creates what is missing
  and never touches what exists. No press tool ever deletes or overwrites a project
  folder, an artifact, or a hand-written note.
- Rendered artifacts AND their sources live under `~/Exports/<project>/` — the whole
  pipeline, not just outputs. Python venvs are the exception: they are disposable,
  never migrated, recreated on demand.

## Document types (press-pdf)

If the user's prompt does not name the type, ASK before anything else:

| Type | Editions | Must ask |
|------|----------|----------|
| `offer` | one | target audience/company if not stated |
| `documentation` | ALWAYS two: `internal` (devs/company — may contain private info) and `external` (public — NO private info, NO internal know-how; lint-gated) | — |
| `legal` | one per signing constellation | ALWAYS: who is the **issuer** and who is the **target**. Given an IČO, resolve via `press ares <IČO>` (ARES registry, cached in config) — never retype company data by hand. |

## Memory layer (Obsidian syntax)

- `PRESS.md` per project: frontmatter + autogen index block between
  `<!-- press:index:start/end -->` markers (CLI-regenerated; human/AI prose outside
  the markers is preserved). One line per artifact: `[[wikilink]] — type — title
  vN — status → target — date`.
- Every PDF artifact gets a sidecar note `<file>.md` (created by `press index add`,
  never overwritten): frontmatter with type/version/issuer/target/status and a
  `supersedes: "[[previous]]"` chain. New versions of a document link their
  predecessor — this is how "make a new version of that offer" resolves.
- Resolving a reference ("that offer", "the NDA v2"): read `PRESS.md` first, follow
  the wikilink to the sidecar note for context, then to the config entry for state.

## Config — `.press.conf.json`

Schema: `press.conf.schema.json` beside this file; the CLI embeds the rules in
`lint`. Top-level: `project` (name/type/description/git/dir), `logo`, `pdf.documents[]`,
`design`, `ares` (IČO cache), `meta` (skillVersion/createdAt/updatedAt).
`press lint --fix` self-corrects fixable drift (missing sections, ids, timestamps,
lost PRESS.md markers); unfixable problems fail with exit 1.

## Versioning

`meta.skillVersion` records which family version last touched the project. When the
family schema evolves, the CLI bumps its embedded version and `lint --fix` migrates
old configs forward. Never migrate by hand.
