package cli

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is set by the build: -ldflags "-X github.com/nick5616/canon/internal/cli.version=v0.1.0".
var version = "dev"

func newVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the canon version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), versionString())
		},
	}
}

func versionString() string {
	s := "canon " + version
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return s
	}
	var rev, dirty string
	for _, kv := range info.Settings {
		switch kv.Key {
		case "vcs.revision":
			rev = kv.Value
		case "vcs.modified":
			if kv.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if rev != "" {
		s += " (" + rev + dirty + ")"
	}
	return s + " " + info.GoVersion
}
