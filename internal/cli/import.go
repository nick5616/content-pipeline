package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/nick5616/canon/engine/importbucket"
)

func newImport() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Draft items from an existing bucket or content.ts",
	}
	cmd.AddCommand(newImportBucket(), &cobra.Command{
		Use:                "content-ts",
		Short:              "Import content.ts entries (M7)",
		DisableFlagParsing: true,
		Annotations:        map[string]string{"stub": "M7"},
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("import content-ts isn't built yet; it lands in M7")
		},
	})
	return cmd
}

func newImportBucket() *cobra.Command {
	var (
		dryRun bool
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "bucket gs://<bucket>[/<prefix>]",
		Short: "Draft an item for every file in an existing bucket (reads it only)",
		Long: `Lists and downloads every object in the bucket, then runs each through
ingest: sanitize, verify, store the clean copy and draft an item. The old
metadata (art-name, art-description, art-date, created, art-order,
private) fills in the drafts, and each top-level folder becomes a draft
collection (dj-crate becomes music). Nothing is written to the bucket.

Rerunning is safe: files already imported keep any edits you made. The
report, which maps each old object to its item, is saved under the state
directory in imports/<bucket>.json.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			bucket, prefix, err := parseBucketURL(args[0])
			if err != nil {
				return err
			}
			cfg, err := ingestConfig(cmd)
			if err != nil {
				return err
			}
			in, err := newIngester(cfg)
			if err != nil {
				return err
			}
			client, err := newGCS(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			defer client.Close()
			im := &importbucket.Importer{GCS: client, Ingest: in, Working: in.Working}
			rep, err := im.Import(cmd.Context(), bucket, importbucket.Options{Prefix: prefix, DryRun: dryRun})
			if err != nil {
				return err
			}
			saved := ""
			if !dryRun {
				if saved, err = saveReport(cfg.StateDir, rep); err != nil {
					return err
				}
			}

			out := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				if err := enc.Encode(rep); err != nil {
					return err
				}
			} else {
				printImport(out, rep, saved)
			}
			if n := rep.Failed(); n > 0 {
				if !asJSON {
					fmt.Fprintf(cmd.ErrOrStderr(), "%d of %d failed; nothing from those files was stored\n", n, len(rep.Results))
				}
				return exitError(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "download, sanitize and verify only; store nothing")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON")
	return cmd
}

// parseBucketURL splits gs://bucket/prefix.
func parseBucketURL(s string) (bucket, prefix string, err error) {
	rest, ok := strings.CutPrefix(s, "gs://")
	if !ok {
		return "", "", fmt.Errorf("%q: want gs://<bucket>[/<prefix>]", s)
	}
	bucket, prefix, _ = strings.Cut(rest, "/")
	if bucket == "" {
		return "", "", fmt.Errorf("%q: no bucket name", s)
	}
	return bucket, prefix, nil
}

func saveReport(stateDir string, rep importbucket.Report) (string, error) {
	dir := filepath.Join(stateDir, "imports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, rep.Bucket+".json")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return "", err
	}
	return p, os.Rename(tmp, p)
}

func printImport(w io.Writer, rep importbucket.Report, saved string) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	counts := map[importbucket.Status]int{}
	warned := 0
	for _, r := range rep.Results {
		counts[r.Status]++
		switch r.Status {
		case importbucket.Failed:
			fmt.Fprintf(tw, "FAIL\t%s\t%s\n", r.Object, r.Error)
		case importbucket.Skipped:
			fmt.Fprintf(tw, "skip\t%s\t%s\n", r.Object, r.Note)
		case importbucket.Checked:
			fmt.Fprintf(tw, "ok\t%s\tclean\n", r.Object)
		case importbucket.Existing:
			fmt.Fprintf(tw, "same\t%s\t%s%s\n", r.Object, r.Note, inCollection(r))
		default:
			fmt.Fprintf(tw, "ok\t%s\tdraft %s%s\n", r.Object, r.Slug, inCollection(r))
		}
		for _, x := range r.Warnings {
			fmt.Fprintf(tw, "  !\t\t%s\n", x)
		}
		if len(r.Warnings) > 0 {
			warned++
		}
	}
	tw.Flush()
	fmt.Fprintf(w, "\n%d imported, %d already there, %d skipped, %d failed", counts[importbucket.Imported],
		counts[importbucket.Existing], counts[importbucket.Skipped], counts[importbucket.Failed])
	if n := counts[importbucket.Checked]; n > 0 {
		fmt.Fprintf(w, ", %d checked (dry run)", n)
	}
	fmt.Fprintf(w, "; %d with warnings to review.\n", warned)
	if saved != "" {
		fmt.Fprintf(w, "Report: %s\nNothing is uploaded until you run canon plan and canon apply.\n", saved)
	}
}

func inCollection(r importbucket.Result) string {
	if r.Collection == "" {
		return ""
	}
	return " in " + r.Collection
}
