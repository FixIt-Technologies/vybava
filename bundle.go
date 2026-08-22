// Package assets exposes Výbava's versioned catalog and installable payloads.
package assets

import "embed"

// FS is the immutable payload shipped inside the Výbava binary.
//
//go:embed catalog/catalog.yaml skills/*/SKILL.md
var FS embed.FS
