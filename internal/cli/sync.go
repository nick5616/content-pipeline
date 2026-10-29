package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/nick5616/canon/engine/store"
)

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

var stateText = map[store.State]string{
	store.InSync:   "in sync with the remote manifest",
	store.Behind:   "behind: the remote moved and you have no local edits (the next plan pulls it)",
	store.Edited:   "local edits on top of the remote manifest",
	store.Diverged: "diverged: the remote moved and you have local edits; see `canon diff`, then `canon pull --discard-local`",
	store.Offline:  "offline: GCS is unreachable (editing still works)",
}

func printStatus(w io.Writer, st store.Status) {
	remote := "unknown"
	if st.RemoteGeneration != nil {
		remote = fmt.Sprint(*st.RemoteGeneration)
	}
	fmt.Fprintf(w, "%s\n  base generation %d, remote generation %s, working revision %s\n",
		stateText[st.State], st.BaseGeneration, remote, st.Revision)
}

func newStatus() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Compare the working manifest with the remote one (one metadata request)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := engineFor(cmd)
			if err != nil {
				return err
			}
			env, err := e.PlanEnv(cmd.Context())
			if err != nil {
				return err
			}
			st, err := e.Working.Status(cmd.Context(), env.Remote)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), st)
			}
			printStatus(cmd.OutOrStdout(), st)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	return cmd
}

func newPull() *cobra.Command {
	var discard bool
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Fetch the remote manifest",
		Long: `Fetches the remote manifest. With no local edits it fast-forwards the
working manifest. With local edits it refuses unless --discard-local is set;
v1 never merges.`,
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
			st, err := e.Working.Pull(cmd.Context(), env.Remote, discard)
			if err != nil {
				return err
			}
			printStatus(cmd.OutOrStdout(), st)
			return nil
		},
	}
	cmd.Flags().BoolVar(&discard, "discard-local", false, "throw away local edits and take the remote manifest")
	return cmd
}

func newDiff() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Show local edits against the remote manifest (as last pulled or applied)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := engineFor(cmd)
			if err != nil {
				return err
			}
			base, gen, err := e.Working.Base()
			if err != nil {
				return err
			}
			working, exists, err := e.Working.Bytes()
			if err != nil {
				return err
			}
			if !exists {
				working = base
			}
			c, err := store.Diff(base, working)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return printJSON(out, c)
			}
			if c.Empty() {
				fmt.Fprintf(out, "No local edits (base generation %d).\n", gen)
				return nil
			}
			fmt.Fprintf(out, "Local edits against generation %d:\n", gen)
			for _, x := range []struct {
				sym  string
				refs []store.ItemRef
			}{{"+", c.Added}, {"~", c.Changed}, {"-", c.Removed}} {
				for _, r := range x.refs {
					fmt.Fprintf(out, "  %s %s  %s\n", x.sym, r.ID, r.Slug)
				}
			}
			if c.Other {
				fmt.Fprintln(out, "  ~ profile, taxonomy, surfaces or owned domains")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	return cmd
}
