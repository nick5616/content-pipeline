package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/nick5616/canon/engine/items"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/store"
)

func newLs() *cobra.Command {
	var (
		f      items.Filter
		kind   string
		vis    string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List items in the working manifest",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := engineFor(cmd)
			if err != nil {
				return err
			}
			m, _, _, err := e.Working.Load()
			if err != nil {
				return err
			}
			f.Kind, f.Visibility = manifest.Kind(kind), manifest.ItemVisibility(vis)
			list := items.List(m, f)
			if asJSON {
				return printJSON(cmd.OutOrStdout(), list)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			for _, it := range list {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", it.ID, it.Slug, it.Kind, it.Visibility, it.Title)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "only this kind")
	cmd.Flags().StringVar(&f.Surface, "surface", "", "only items placed on this surface")
	cmd.Flags().StringVar(&vis, "visibility", "", "draft or public")
	cmd.Flags().StringVar(&f.Tag, "tag", "", "only items with this tag")
	cmd.Flags().StringVarP(&f.Query, "query", "q", "", "match title, slug or aliases")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	return cmd
}

func newShow() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id|slug|alias>",
		Short: "Print one item as JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := engineFor(cmd)
			if err != nil {
				return err
			}
			m, _, _, err := e.Working.Load()
			if err != nil {
				return err
			}
			i, err := items.Find(m, args[0])
			if err != nil {
				return err
			}
			return printJSON(cmd.OutOrStdout(), m.Items[i])
		},
	}
}

// assignments turns key=value arguments into a JSON object. A value that
// parses as JSON is used as JSON (numbers, true, null, arrays, objects);
// anything else is a string. Dotted keys nest: date.value=2025-06.
func assignments(args []string) (map[string]any, error) {
	out := map[string]any{}
	for _, a := range args {
		k, v, ok := strings.Cut(a, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("%q: want key=value", a)
		}
		var val any
		if err := json.Unmarshal([]byte(v), &val); err != nil {
			val = v
		}
		parts := strings.Split(k, ".")
		m := out
		for _, p := range parts[:len(parts)-1] {
			next, ok := m[p].(map[string]any)
			if !ok {
				next = map[string]any{}
				m[p] = next
			}
			m = next
		}
		m[parts[len(parts)-1]] = val
	}
	return out, nil
}

func newItem() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "item",
		Short: "Create, edit or remove items in the working manifest",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "new <kind> <slug> <title> [key=value...]",
		Short: "Add a draft item",
		Args:  cobra.MinimumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := engineFor(cmd)
			if err != nil {
				return err
			}
			fields, err := assignments(args[3:])
			if err != nil {
				return err
			}
			fields["kind"], fields["slug"], fields["title"] = args[0], args[1], args[2]
			body, _ := json.Marshal(fields)
			it, _, err := items.New(e.Working, "", body)
			if err != nil {
				return explain(cmd.ErrOrStderr(), err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added draft %s (%s)\n", it.Slug, it.ID)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "set <id|slug|alias> (key=value... | -)",
		Short: "Edit an item: key=value pairs, or a JSON merge patch on stdin with -",
		Long: `Edits one item. Values that parse as JSON are JSON, so
  canon item set sketch visibility=public tags='["ink"]' date.value=2025-06
and key=null removes a field. With - the merge patch (RFC 7396) is read
from stdin. The result must validate, or nothing changes.`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := engineFor(cmd)
			if err != nil {
				return err
			}
			var patch []byte
			if len(args) == 2 && args[1] == "-" {
				if patch, err = io.ReadAll(cmd.InOrStdin()); err != nil {
					return err
				}
			} else {
				fields, err := assignments(args[1:])
				if err != nil {
					return err
				}
				patch, _ = json.Marshal(fields)
			}
			it, _, err := items.Patch(e.Working, "", args[0], patch)
			if err != nil {
				return explain(cmd.ErrOrStderr(), err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "updated %s\n", it.Slug)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "rm <id|slug|alias>",
		Short: "Remove a draft (anything public in the remote manifest is refused: unpublish it instead)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := engineFor(cmd)
			if err != nil {
				return err
			}
			if _, err := items.Remove(e.Working, "", args[0]); err != nil {
				return explain(cmd.ErrOrStderr(), err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", args[0])
			return nil
		},
	})
	return cmd
}

func newTaxonomy() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "taxonomy",
		Short: "List or edit categories, tech, mediums, tags and viewports",
	}
	var asJSON bool
	ls := &cobra.Command{
		Use:   "ls [vocab]",
		Short: "List taxonomy terms",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := engineFor(cmd)
			if err != nil {
				return err
			}
			m, _, _, err := e.Working.Load()
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), m.Taxonomy)
			}
			var all map[string][]map[string]any
			b, _ := json.Marshal(m.Taxonomy)
			_ = json.Unmarshal(b, &all)
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			for _, vocab := range items.Vocabs {
				if len(args) == 1 && args[0] != vocab {
					continue
				}
				for _, t := range all[vocab] {
					fmt.Fprintf(tw, "%s\t%v\t%v\n", vocab, t["slug"], t["label"])
				}
			}
			return tw.Flush()
		},
	}
	ls.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	cmd.AddCommand(ls)
	cmd.AddCommand(&cobra.Command{
		Use:   "set <vocab> <slug> label=... [icon=...] [width=... height=...]",
		Short: "Create or update one term",
		Args:  cobra.MinimumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := engineFor(cmd)
			if err != nil {
				return err
			}
			fields, err := assignments(args[2:])
			if err != nil {
				return err
			}
			fields["slug"] = args[1]
			body, _ := json.Marshal(fields)
			if _, _, err := items.SetTerm(e.Working, "", args[0], args[1], body); err != nil {
				return explain(cmd.ErrOrStderr(), err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "saved %s/%s\n", args[0], args[1])
			return nil
		},
	})
	return cmd
}

func newSurface() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "surface",
		Short: "Declare the surfaces items can be placed on (canon view lists them)",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "set <id> label=... [baseUrl=...]",
		Short: "Declare or update one surface",
		Long: `Adds a surface to the working manifest's surfaces table, or updates its
label and base URL. Only registered surfaces (those with schemas) are
accepted: nickel-desktop, software-portfolio, design-portfolio,
art-portfolio, holodeck-showroom, holodeck-art-room, holodeck-dj-room.`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := engineFor(cmd)
			if err != nil {
				return err
			}
			fields, err := assignments(args[1:])
			if err != nil {
				return err
			}
			fields["id"] = args[0]
			body, _ := json.Marshal(fields)
			if _, _, err := items.SetSurface(e.Working, "", args[0], body); err != nil {
				return explain(cmd.ErrOrStderr(), err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "saved surface %s\n", args[0])
			return nil
		},
	})
	return cmd
}

// explain prints validation problems one per line before the error.
func explain(w io.Writer, err error) error {
	if ps := problemsOf(err); len(ps) > 0 {
		for _, p := range ps {
			fmt.Fprintf(w, "  %s\n", p)
		}
		return fmt.Errorf("nothing changed: the result would be invalid")
	}
	return err
}

func problemsOf(err error) []manifest.Problem {
	var inv *store.InvalidError
	if errors.As(err, &inv) {
		return inv.Problems
	}
	return nil
}
