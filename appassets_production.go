//go:build production

package appassets

import (
	"embed"
	"io/fs"
)

//go:embed all:frontend/dist
var embeddedDist embed.FS

// FS returns the production asset filesystem embedded into the binary.
func FS() fs.FS {
	dist, err := fs.Sub(embeddedDist, "frontend/dist")
	if err != nil {
		panic(err)
	}
	return dist
}
