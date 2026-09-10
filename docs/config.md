# vybava config — the shared per-repo configuration

Every applet that needs repository-specific settings reads them from one
file at the repo root, `vybava.config.ts`, through `internal/vconfig`. The
file is TypeScript so a repo gets completion, types and the freedom to
compute its config; bun evaluates it and the applets consume the resulting
JSON. A repo without bun ships `vybava.config.json` with the same shape.

```text
vybava config init            # scaffold vybava.config.ts + .vybava/config.ts
vybava config check --json    # evaluate, verify the helpers match this binary — CI gate
vybava config show --json     # the evaluated document
```

`.vybava/config.ts` is generated: it carries `defineConfig` and the typed
helpers each section understands (`englishAsKey`, `pathKeys`, …). It is
committed, never edited, and `vybava config check` fails on drift so an
upgraded binary and an old helper file cannot disagree silently;
`vybava config init --force` rewrites it.

```ts
import { defineConfig, englishAsKey, pathKeys } from './.vybava/config';

export default defineConfig({
  lok: {
    catalogs: {
      mobile: englishAsKey('apps/client/locales/{locale}.json', ['en', 'cs'], {
        required: ['en', 'cs'],
        afterWrite: 'bun run i18n:types',
        scan: { roots: ['apps/client'] },
      }),
      webDictionaries: pathKeys('apps/web/dictionaries/{locale}.json', ['en', 'cs', 'sk', 'uk'], {
        required: ['en', 'cs'],
      }),
    },
  },
});
```

Evaluation is cached next to the git metadata (`<git-dir>/vybava-config-cache.json`)
keyed by the file's size and mtime, so hooks and repeated calls pay bun's
~0.5 s once per edit. Sections use `DisallowUnknownFields`: a typo in a key
is a diagnostic, never a silently ignored setting. Adding a section for a
new applet means a Go struct in that applet's package, its TypeScript twin
in `internal/vconfig/config-helpers.ts`, and nothing else.
