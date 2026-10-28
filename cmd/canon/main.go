// Command canon manages Nicole's content: one manifest, sanitized blobs, and
// per-surface views published to GCS.
package main

import (
	"os"

	"github.com/nick5616/canon/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
