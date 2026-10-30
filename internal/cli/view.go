package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newView() *cobra.Command {
	var (
		drafts bool
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "view [surface]",
		Short: "List surfaces, or print the view one would get (nothing is published)",
		Long: `With no argument, lists the surfaces the working manifest declares and
how many placements each has.

With a surface, prints the view it would get from the working manifest:
what the next canon apply would publish, edits included. --drafts shows
placed drafts too, as if they were public, for review before publishing.
URLs point where the files would be published; warnings (entries left out
because their files aren't ready) go to stderr.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := engineFor(cmd)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if len(args) == 0 {
				ss, err := e.Surfaces()
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(w, ss)
				}
				if len(ss) == 0 {
					fmt.Fprintln(w, "The working manifest declares no surfaces yet.")
					return nil
				}
				tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
				for _, s := range ss {
					reg := "not registered"
					if s.Registered {
						reg = "registered"
					}
					fmt.Fprintf(tw, "%s\t%s\t%d placements\t%s\n", s.ID, s.Label, s.Placements, reg)
				}
				return tw.Flush()
			}
			b, ps, err := e.Preview(cmd.Context(), args[0], drafts)
			if err != nil {
				return err
			}
			for _, p := range ps {
				fmt.Fprintf(cmd.ErrOrStderr(), "  ! %s\n", p)
			}
			_, err = w.Write(b)
			return err
		},
	}
	cmd.Flags().BoolVar(&drafts, "drafts", false, "include placed drafts, as if they were public")
	cmd.Flags().BoolVar(&asJSON, "json", false, "list surfaces as JSON")
	return cmd
}
