package claudeguards

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/henderson-tech/vybava/internal/vconfig"
)

// ---------------------------------------------------------------------------
// context:locale-catalog — a file that vybava.config.ts declares as a `lok`
// catalog is never read raw, not even a 200-line range: the catalog is
// queried through `lok get/grep` and written through `lok add/set/rm`, which
// keep every locale in sync. The config is read through vconfig's cache, so
// the hook pays the bun evaluation once per config edit.
// ---------------------------------------------------------------------------

var (
	catalogOnce  sync.Once
	catalogFiles []string // absolute paths of every configured catalog file
)

// lokCatalogFiles resolves the configured catalog files for cwd, once.
func lokCatalogFiles(cwd string) []string {
	catalogOnce.Do(func() {
		cfg, err := vconfig.Load(cwd)
		if err != nil {
			return
		}
		var lok struct {
			Catalogs map[string]struct {
				Files   string   `json:"files"`
				Locales []string `json:"locales"`
			} `json:"catalogs"`
		}
		// Unknown fields are fine here — the guard only needs paths.
		if raw, ok := cfg.Sections["lok"]; ok {
			_ = jsonUnmarshalLoose(raw, &lok)
		}
		for _, c := range lok.Catalogs {
			for _, loc := range c.Locales {
				catalogFiles = append(catalogFiles, filepath.Join(cfg.Root, strings.ReplaceAll(c.Files, "{locale}", loc)))
			}
		}
	})
	return catalogFiles
}

func isLokCatalog(abs, cwd string) bool {
	if !strings.HasSuffix(abs, ".json") {
		return false
	}
	for _, f := range lokCatalogFiles(cwd) {
		if f == abs {
			return true
		}
	}
	return false
}

const catalogMsg = `%s is a locale catalog managed by lok (vybava.config.ts → lok.catalogs).
Reading it raw floods the context with thousands of strings you will not use,
and editing it by hand desynchronises the other locales.

Query:  lok get '<key>' --json   ·   lok grep '<pattern>' --json   ·   lok missing --json
Write:  lok add '<key>' --tr cs='…' --json   ·   lok set … · lok rm …
Source: lok scan --json   (missing literal keys + probable orphans)`

func catalogDenial(abs string) *Denial {
	return deny("context:locale-catalog", fmt.Sprintf(catalogMsg, abs), contextReadEscape)
}
