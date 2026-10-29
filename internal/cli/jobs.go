package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/nick5616/canon/engine/jobs"
)

func newJobs() *cobra.Command {
	var (
		state  string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "jobs",
		Short: "List derived-asset jobs (thumbnails, previews, beat grids...)",
		Long: `Lists the jobs that have run or are queued on this machine, newest
first. canon plan runs pending jobs itself; canon jobs run runs them now.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := engineFor(cmd)
			if err != nil {
				return err
			}
			js, err := e.Jobs.List(jobs.State(state))
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if asJSON {
				return printJSON(w, js)
			}
			tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			for _, j := range js {
				line := fmt.Sprintf("%s\t%s\t%s\t%s", j.Jobkey[:12], j.Recipe, j.State, j.Source[:len("sha256:")+12])
				if j.LastError != nil {
					line += "\t" + firstLine(*j.LastError)
				}
				fmt.Fprintln(tw, line)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			if len(js) == 0 {
				fmt.Fprintln(w, "No jobs yet.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&state, "state", "", "only jobs in this state: pending, running, done or failed")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print jobs as JSON")
	cmd.AddCommand(newJobsRun(), newJobsRetry())
	return cmd
}

func newJobsRun() *cobra.Command {
	var (
		o      jobs.Options
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run every job the working manifest needs, here",
		Long: `Runs each needed job that isn't in the local cache or already in the
private bucket (another machine ran it), failed ones included. Outputs
land in the local cache and facts fill only empty fields (D48); nothing
is uploaded until canon plan and canon apply.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := engineFor(cmd)
			if err != nil {
				return err
			}
			o.Failed = true
			if !asJSON {
				w := cmd.ErrOrStderr()
				o.Progress = func(j jobs.Job) {
					if j.State == jobs.Failed {
						fmt.Fprintf(w, "FAIL %s %s: %s\n", j.Recipe, j.Jobkey[:12], firstLine(deref(j.LastError)))
					}
				}
			}
			rep, err := e.Runner().Run(cmd.Context(), o)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), rep)
			}
			printJobReport(cmd.OutOrStdout(), rep)
			if rep.Failed > 0 {
				return exitError(1)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&o.Item, "item", "", "only this item (id or slug)")
	cmd.Flags().StringVar(&o.Recipe, "recipe", "", "only this recipe (thumb, lqip, palette, poster, web, preview, peaks, dj-mp3, djgrid)")
	cmd.Flags().BoolVar(&o.RefreshFacts, "refresh-facts", false, "overwrite facts the recipes own, even ones already set")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON")
	return cmd
}

func newJobsRetry() *cobra.Command {
	return &cobra.Command{
		Use:   "retry <jobkey>",
		Short: "Queue a failed job again (it runs on the next canon plan or jobs run)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := engineFor(cmd)
			if err != nil {
				return err
			}
			j, err := e.Jobs.Retry(args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s is pending again\n", j.Recipe, j.Jobkey[:12])
			return nil
		},
	}
}

// printJobReport says what a run did. Quiet when it did nothing new, so a
// plan with every job done prints only the plan.
func printJobReport(w io.Writer, r jobs.Report) {
	if r.Ran+r.Failed+r.Skipped+r.Facts == 0 {
		return
	}
	fmt.Fprintf(w, "Jobs: %d ran, %d already here, %d already in the bucket, %d failed", r.Ran, r.Cached, r.Remote, r.Failed)
	if r.Skipped > 0 {
		fmt.Fprintf(w, ", %d failed before (canon jobs run retries them)", r.Skipped)
	}
	if r.Facts > 0 {
		fmt.Fprintf(w, "; facts filled on %d items", r.Facts)
	}
	fmt.Fprintln(w, ".")
	for _, e := range r.Errors {
		fmt.Fprintf(w, "  ! %s\n", e)
	}
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return l
}
