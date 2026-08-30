package press

import (
	_ "embed"
)

// The press family's shared law travels inside the binary rather than as a
// file path into some checkout. Three skills depend on it; a path would rot
// the moment the repository moved, which is exactly what happened to the
// standalone press repository this package replaces.

//go:embed doctrine/CONVENTIONS.md
var conventions string

//go:embed doctrine/press.conf.schema.json
var confSchema string

// Conventions is the shared law of the press family: directory structure,
// config layout, index and memory syntax.
func Conventions() string { return conventions }

// ConfSchema is the JSON Schema for .press.conf.json. The CLI is the single
// enforcement point for it, so skills validate by calling press rather than by
// reimplementing the rules.
func ConfSchema() string { return confSchema }
