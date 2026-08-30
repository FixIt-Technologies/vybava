// Package assets exposes Výbava's versioned catalog and installable payloads.
package assets

import "embed"

// FS is the immutable payload shipped inside the Výbava binary.
//
// The bare "skills" pattern walks the whole payload tree — a skill is not
// always a lone SKILL.md; press-pdf ships references/ and an assets/ pipeline.
// Without the all: prefix Go skips _ and . prefixed entries, which keeps
// __pycache__ and editor droppings out of the binary for free.
//
//go:embed catalog/catalog.yaml skills
var FS embed.FS
