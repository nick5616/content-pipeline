package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/nick5616/canon/engine/blobstore"
	"github.com/nick5616/canon/engine/config"
	"github.com/nick5616/canon/engine/ingest"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/store"
	"github.com/nick5616/canon/engine/surfaces"
)

func newIngest() *cobra.Command {
	var (
		kind   string
		dryRun bool
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "ingest <files or folders>...",
		Short: "Sanitize files, store them as blobs and draft items (local only)",
		Long: `Strips location, device and owner metadata from each file, verifies
nothing private survived, stores the clean copy in the local blob cache and
drafts an item for it in the working manifest. Originals are only read.
Folders are walked recursively; hidden files are skipped. Nothing is
uploaded: the next plan and apply do that.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := ingestConfig(cmd)
			if err != nil {
				return err
			}
			in, err := newIngester(cfg)
			if err != nil {
				return err
			}
			paths, err := expand(args)
			if err != nil {
				return err
			}
			opts := ingest.Options{Kind: manifest.Kind(kind), DryRun: dryRun}

			var results []ingest.FileResult
			out := cmd.OutOrStdout()
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			for _, p := range paths {
				var r ingest.FileResult
				o, err := in.StagePath(p)
				if err != nil {
					msg := err.Error()
					r = ingest.FileResult{Name: filepath.Base(p), Stage: ingest.Failed, Error: &msg}
				} else {
					r = in.Process(cmd.Context(), o, opts, nil)
				}
				r.Name = p
				results = append(results, r)
				if !asJSON {
					printResult(tw, r, dryRun)
					tw.Flush()
				}
			}
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				if err := enc.Encode(results); err != nil {
					return err
				}
			}
			failed := 0
			for _, r := range results {
				if r.Stage == ingest.Failed {
					failed++
				}
			}
			if failed > 0 {
				if !asJSON {
					fmt.Fprintf(cmd.ErrOrStderr(), "%d of %d failed; nothing from those files was stored\n", failed, len(results))
				}
				return exitError(1)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "item kind instead of the default (artwork or photo for images)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "detect, sanitize and verify only; store nothing")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print results as JSON")
	return cmd
}

func printResult(tw *tabwriter.Writer, r ingest.FileResult, dry bool) {
	switch {
	case r.Stage == ingest.Failed:
		fmt.Fprintf(tw, "FAIL\t%s\t%s\n", r.Name, deref(r.Error))
	case dry:
		fmt.Fprintf(tw, "ok\t%s\tclean (%s)\n", r.Name, shortRef(deref(r.Blob)))
	case r.Note != nil:
		fmt.Fprintf(tw, "same\t%s\t%s\n", r.Name, *r.Note)
	default:
		fmt.Fprintf(tw, "ok\t%s\tdraft %s (%s)\n", r.Name, r.Slug, shortRef(deref(r.Blob)))
	}
}

func shortRef(ref string) string {
	if len(ref) > len("sha256:")+12 {
		return ref[:len("sha256:")+12]
	}
	return ref
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ingestConfig loads config; a missing file is fine (defaults), an invalid
// one is not.
func ingestConfig(cmd *cobra.Command) (config.Config, error) {
	cfg, path, err := loadConfig(cmd)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return cfg, err
	}
	if ps := cfg.Validate(); len(ps) > 0 {
		return cfg, fmt.Errorf("%s: %v", path, ps)
	}
	return cfg, nil
}

func newIngester(cfg config.Config) (*ingest.Ingester, error) {
	reg, err := surfaces.Default()
	if err != nil {
		return nil, err
	}
	blobs, err := blobstore.Open(cfg.CacheDir)
	if err != nil {
		return nil, err
	}
	return ingest.New(blobs, store.Open(cfg.StateDir, cfg.ProfileName, reg), cfg.MaxIngestBytes), nil
}

// expand turns arguments into files: folders are walked, hidden entries
// and symlinks skipped, and the result sorted so runs are repeatable.
func expand(args []string) ([]string, error) {
	var out []string
	for _, a := range args {
		fi, err := os.Stat(a)
		if err != nil {
			return nil, err
		}
		if !fi.IsDir() {
			out = append(out, a)
			continue
		}
		var found []string
		err = filepath.WalkDir(a, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if p != a && strings.HasPrefix(d.Name(), ".") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type().IsRegular() {
				found = append(found, p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		sort.Strings(found)
		out = append(out, found...)
	}
	if len(out) == 0 {
		return nil, errors.New("no files to ingest")
	}
	return out, nil
}
