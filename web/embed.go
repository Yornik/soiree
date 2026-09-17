// Package web holds the frontend sources, embedded into the binary.
//
// The sources are embedded as-is and processed at startup (hashed, compressed,
// templated) rather than by a separate build step. That keeps `go build` on a
// fresh clone sufficient to produce a working binary, with the Go compiler as
// the only build dependency.
package web

import (
	"embed"
	"io/fs"
)

//go:embed src
var srcRoot embed.FS

// FS returns the frontend source tree rooted at src/.
func FS() fs.FS {
	sub, err := fs.Sub(srcRoot, "src")
	if err != nil {
		// Unreachable: the embed directive above guarantees src/ exists.
		panic(err)
	}
	return sub
}
