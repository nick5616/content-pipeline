package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// stubs are commands that exist so the CLI surface is visible from M0, but
// which later milestones implement.
func stubs() []*cobra.Command {
	var out []*cobra.Command
	for _, s := range []struct{ use, short, milestone string }{
		{"gc", "Delete unreferenced blobs", "M8"},
		{"rollback", "Restore an earlier manifest", "M8"},
	} {
		out = append(out, &cobra.Command{
			Use:                s.use,
			Short:              s.short + " (" + s.milestone + ")",
			DisableFlagParsing: true,
			Annotations:        map[string]string{"stub": s.milestone},
			RunE: func(*cobra.Command, []string) error {
				return fmt.Errorf("%s isn't built yet; it lands in %s", s.use, s.milestone)
			},
		})
	}
	return out
}
