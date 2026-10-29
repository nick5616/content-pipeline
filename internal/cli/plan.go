package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nick5616/canon/engine/apply"
	"github.com/nick5616/canon/engine/jobs"
	"github.com/nick5616/canon/engine/plan"
	"github.com/nick5616/canon/engine/store"
)

func newPlan() *cobra.Command {
	var (
		out    string
		asJSON bool
		noJobs bool
	)
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Compute what apply would change, and save it",
		Long: `Compares the working manifest and local cache with what's in the
private and archive buckets (listings only, no downloads), and saves the
result as a plan. Nothing in the cloud changes until you run canon apply
with the plan's id. If the remote manifest moved and you have no local
edits, plan pulls it first.

First it runs any derived jobs that haven't run (thumbnails, palettes,
previews, beat grids...), locally; --no-jobs skips that, and the plan
then warns about jobs it can't upload yet.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := engineFor(cmd)
			if err != nil {
				return err
			}
			env, err := e.PlanEnv(cmd.Context())
			if err != nil {
				return err
			}
			if noJobs {
				env.RunJobs = nil
			} else {
				e.OnJobs = func(r jobs.Report) { printJobReport(cmd.ErrOrStderr(), r) }
			}
			p, err := plan.Make(cmd.Context(), env)
			var inv *store.InvalidError
			if errors.As(err, &inv) {
				for _, pr := range inv.Problems {
					fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", pr)
				}
				return errors.New("no plan: fix the errors above")
			}
			if err != nil {
				return err
			}
			if _, err := plan.Save(e.StateDir, p); err != nil {
				return err
			}
			if out != "" {
				if err := plan.WriteFile(out, p); err != nil {
					return err
				}
			}
			w := cmd.OutOrStdout()
			if asJSON {
				return printJSON(w, p)
			}
			if err := plan.Render(w, p); err != nil {
				return err
			}
			if !p.Empty() {
				fmt.Fprintf(w, "\nSaved. Apply exactly this with: canon apply %s\n", p.ID)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "out", "o", "", "also write the plan to this file")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the plan as JSON")
	cmd.Flags().BoolVar(&noJobs, "no-jobs", false, "don't run pending jobs first")
	cmd.AddCommand(&cobra.Command{
		Use:   "show <id|file>",
		Short: "Print a saved plan",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := engineFor(cmd)
			if err != nil {
				return err
			}
			p, err := plan.Open(e.StateDir, args[0])
			if err != nil {
				return err
			}
			return plan.Render(cmd.OutOrStdout(), p)
		},
	})
	return cmd
}

func newApply() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apply <plan id|file>",
		Short: "Apply a saved plan, and nothing else",
		Long: `Runs the ops of a saved plan, in order. It refuses if the remote
manifest or the working manifest changed since the plan was made; run
canon plan again then. If it stops partway, everything done so far is
safe to keep, and the next plan lists only what's left.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := engineFor(cmd)
			if err != nil {
				return err
			}
			p, err := plan.Open(e.StateDir, args[0])
			if err != nil {
				return err
			}
			deps, err := e.Deps(cmd.Context())
			if err != nil {
				return err
			}
			var writes []plan.Op
			for _, o := range p.Ops {
				if o.Writes() {
					writes = append(writes, o)
				}
			}
			w := cmd.OutOrStdout()
			last := 0
			st, err := apply.Apply(cmd.Context(), p, deps, func(s apply.Status) {
				for ; last < s.Done; last++ {
					o := writes[last]
					fmt.Fprintf(w, "  [%d/%d] %-16s %s\n", last+1, s.Total, o.Type, o.Path)
				}
			})
			if err != nil {
				if st.Done > 0 {
					fmt.Fprintf(w, "Stopped after %d of %d. What's done is safe; run canon plan to see what's left.\n", st.Done, st.Total)
				}
				return err
			}
			if st.NewGeneration != nil {
				fmt.Fprintf(w, "Applied. The remote manifest is now generation %d.\n", *st.NewGeneration)
			} else {
				fmt.Fprintln(w, "Applied.")
			}
			return nil
		},
	}
	return cmd
}
