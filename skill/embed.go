// Package skill embeds the yonder agent skill so the binary can install it.
package skill

import "embed"

// FS holds the yonder/ skill folder.
//
//go:embed yonder
var FS embed.FS
