// Package std embeds the sources of Lyth's standard library, the packages a
// Lyth program imports as lyth/<name> (lyth/coll). The lyth command compiles
// them like the program's own packages and adds them to the build as the
// module "lyth"; see ADR 0030.
package std

import "embed"

// FS holds the library's Lyth sources, one directory per package.
//
//go:embed coll/*.lyth
var FS embed.FS
