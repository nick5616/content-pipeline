package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/nick5616/canon/engine/doctor"
)

func newDoctor() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check tools, config, credentials and buckets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, path, err := loadConfig(cmd)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				// Still run every other check against defaults.
				cfg = defaultsFor(cfg)
			}
			r := doctor.Run(cmd.Context(), doctor.NewEnv(cfg, path, err))

			out := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				if err := enc.Encode(r); err != nil {
					return err
				}
			} else {
				tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
				for _, c := range r.Checks {
					fmt.Fprintf(tw, "%s\t%s\t%s\n", mark(c.Status), c.Name, c.Detail)
				}
				tw.Flush()
			}
			if r.Failed() {
				return exitError(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON")
	return cmd
}

func mark(s doctor.Status) string {
	switch s {
	case doctor.OK:
		return "ok  "
	case doctor.Warn:
		return "WARN"
	case doctor.Fail:
		return "FAIL"
	default:
		return "skip"
	}
}
