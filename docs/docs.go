// Package docs embeds the documentation that lyth guide prints, so that
// what an AI agent (or a person) reads describes the installed compiler.
package docs

import "embed"

// FS holds the guide, the specification, and the collection design.
//
//go:embed guide/*.md spec/*.md design/collections.md
var FS embed.FS
