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
		{"status", "Show sync status of the working manifest", "M3"},
		{"pull", "Fetch the remote manifest", "M3"},
		{"diff", "Show local edits against the remote manifest", "M3"},
		{"ls", "List items", "M3"},
		{"show", "Show one item", "M3"},
		{"item", "Create, edit or remove items", "M3"},
		{"taxonomy", "Edit categories, tech, mediums and tags", "M3"},
		{"plan", "Compute what apply would change", "M3"},
		{"apply", "Apply a saved plan", "M3"},
		{"jobs", "List or retry derived-asset jobs", "M4"},
		{"view", "Print a surface's resolved view", "M5"},
		{"import", "Import content.ts or a bucket", "M7"},
		{"gc", "Delete unreferenced blobs", "M8"},
		{"rollback", "Restore an earlier manifest", "M8"},
	} {
		out = append(out, &cobra.Command{
			Use:                s.use,
			Short:              s.short + " (" + s.milestone + ")",
			DisableFlagParsing: true,
			RunE: func(*cobra.Command, []string) error {
				return fmt.Errorf("%s is not in M0; it lands in %s", s.use, s.milestone)
			},
		})
	}
	return out
}
