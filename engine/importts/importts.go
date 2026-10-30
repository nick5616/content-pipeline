// Package importts drafts items from Nickel Tools' content.ts (M7): the
// projects behind the desktop, the software, UX and art portfolio pages and
// the 3D showroom. content.ts is read statically (extract.mjs, through the
// TypeScript compiler API; nothing in it runs), then mapped here:
//
//	entry                 -> item (project, or experience for places in the 3D world)
//	surfaces + *Only      -> placements on nickel-desktop, software-portfolio,
//	                         design-portfolio, art-portfolio and holodeck-showroom
//	*Override             -> per-surface copy
//	hide: true / comments -> draft, private.notes
//	thumbnail             -> a sanitized thumbnail asset
//
// Everything lands as a draft: you review `canon view --drafts`, then
// publish. Rerunning only adds what's new; items already in the manifest
// keep your edits.
package importts

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/nick5616/canon/engine/ids"
	"github.com/nick5616/canon/engine/importbucket"
	"github.com/nick5616/canon/engine/ingest"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/store"
)

//go:embed extract.mjs
var extractScript []byte

// Source is content.ts as extract.mjs prints it.
type Source struct {
	Content  []Entry             `json:"content"`
	Featured []string            `json:"featured"`
	Icons    map[string]string   `json:"icons"`
	Taxonomy map[string][]string `json:"taxonomy"`
	Profile  *Profile            `json:"profile"`
}

// Entry is one content.ts entry.
type Entry struct {
	ID            string         `json:"id"`
	Hide          bool           `json:"hide"`
	Title         string         `json:"title"`
	Description   string         `json:"description"`
	Category      string         `json:"category"`
	Status        string         `json:"status"`
	Date          string         `json:"date"`
	Thumbnail     string         `json:"thumbnail"`
	Why           string         `json:"why"`
	URL           string         `json:"url"`
	Route         string         `json:"route"`
	Source        string         `json:"source"`
	BackendSource string         `json:"backendSource"`
	Tech          []string       `json:"tech"`
	Mediums       []string       `json:"mediums"`
	Tags          []string       `json:"tags"`
	Surfaces      []string       `json:"surfaces"`
	DesktopOnly   *DesktopOnly   `json:"desktopOnly"`
	PortfolioOnly *PortfolioOnly `json:"portfolioOnly"`
	Overrides     map[string]map[string]json.RawMessage
	Comments      []string `json:"$comments"`
	Section       string   `json:"$section"`
}

// UnmarshalJSON gathers the *Override fields by surface.
func (e *Entry) UnmarshalJSON(b []byte) error {
	type plain Entry
	if err := json.Unmarshal(b, (*plain)(e)); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	e.Overrides = map[string]map[string]json.RawMessage{}
	for k, v := range raw {
		if s, ok := strings.CutSuffix(k, "Override"); ok {
			var o map[string]json.RawMessage
			if err := json.Unmarshal(v, &o); err != nil {
				return fmt.Errorf("%s.%s: %w", e.ID, k, err)
			}
			e.Overrides[s] = o
		}
	}
	return nil
}

// DesktopOnly is an entry's desktop-only settings.
type DesktopOnly struct {
	Title                 string `json:"title"`
	OpenInNewTab          *bool  `json:"openInNewTab"`
	WindowWidth           int    `json:"windowWidth"`
	WindowHeight          int    `json:"windowHeight"`
	ContentfulDescription *struct {
		Ident string `json:"$ident"`
	} `json:"contentfulDescription"`
}

// PortfolioOnly is an entry's portfolio-only settings.
type PortfolioOnly struct {
	Preview     json.RawMessage `json:"preview"`
	ArtGrouping string          `json:"artGrouping"`
}

// Profile is profile.ts's PROFILE.
type Profile struct {
	Name     string            `json:"name"`
	Headline string            `json:"headline"`
	Summary  string            `json:"summary"`
	Links    map[string]string `json:"links"`
}

// Extract runs extract.mjs on contentPath with Node. tsDir is a directory
// whose node_modules has typescript (Nickel Tools' own, or Canon's client).
func Extract(ctx context.Context, contentPath, tsDir string) (*Source, error) {
	dir, err := os.MkdirTemp("", "canon-extract-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	script := filepath.Join(dir, "extract.mjs")
	if err := os.WriteFile(script, extractScript, 0o600); err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "node", script, tsDir, contentPath)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("extract %s: %v: %s", contentPath, err, strings.TrimSpace(stderr.String()))
	}
	var src Source
	if err := json.Unmarshal(out, &src); err != nil {
		return nil, fmt.Errorf("extract %s: %w", contentPath, err)
	}
	return &src, nil
}

// --- The mapping's fixed calls ------------------------------------------

// skipped are app chrome, not content (M0-M1 §0.1).
var skipped = map[string]bool{"about": true, "contact": true, "settings": true}

// renamed: content.ts ids whose Canon slug differs; the id stays an alias.
var renamed = map[string]string{"videogamequest": "rpg-quests"}

// collections: the art entries that are the halls the bucket import made.
var collections = map[string]string{
	"art-digital-art": "digitalart", "art-paintings": "paintings", "art-sketches": "sketches",
	"art-lefthanded": "lefthanded", "art-miscellaneous": "miscellaneous", "art-notesappart": "notesappart",
}

// experiences are places inside the 3D world, and the surface they're in.
var experiences = map[string]string{
	"art-room": "holodeck-art-room", "art-museum": "", "software-showroom": "holodeck-showroom",
	"courage-computer": "", "math-room": "",
}

// categoryMerges (D59).
var categoryMerges = map[string]string{"Games": "Gaming", "Social Tools": "Social", "Social App": "Social"}

// rankAfter: a ranks just after b wherever both are placed (D59).
var rankAfter = map[string]string{"smart-piano": "midimuse", "toy": "big"}

// urlRewrites: domains given up, and where those projects live now (D59, D61).
var urlRewrites = map[string]string{"videogamequest.me": "rpg-quests.netlify.app", "tierlistify.com": "tierlistify.netlify.app"}

// Owned domains (D59); netlify.app and github.io hosts the content links to
// are added exactly.
var ownedDomains = []string{
	"nicolebelovoskey.com", "*.nicolebelovoskey.com", "nickbelovoskey.com", "*.nickbelovoskey.com",
	"nickeltools.dev", "*.nickeltools.dev", "saucedog.art", "*.saucedog.art",
	"friendex.online", "*.friendex.online",
}

// The surfaces content.ts feeds, with where they live.
var surfaceFor = map[string]string{
	"desktop": "nickel-desktop", "software": "software-portfolio", "design": "design-portfolio", "art": "art-portfolio",
}

var declared = []manifest.Surface{
	{ID: "nickel-desktop", Label: "Nickel Tools desktop", BaseURL: httpURL("https://nickeltools.dev/desktop")},
	{ID: "software-portfolio", Label: "Software portfolio", BaseURL: httpURL("https://nickeltools.dev/portfolio/software")},
	{ID: "design-portfolio", Label: "UX portfolio", BaseURL: httpURL("https://nickeltools.dev/portfolio/ux")},
	{ID: "art-portfolio", Label: "Art portfolio", BaseURL: httpURL("https://nickeltools.dev/portfolio/art")},
	{ID: "holodeck-showroom", Label: "Software Showroom", BaseURL: httpURL("https://nicolebelovoskey.com/software")},
}

func httpURL(s string) *manifest.HttpURL { u := manifest.HttpURL(s); return &u }

// NickelToolsBase is where content.ts's relative routes and thumbnails live.
const NickelToolsBase = "https://nickeltools.dev"

// --- Import ---------------------------------------------------------------

// Status is one entry's outcome.
type Status string

const (
	Imported Status = "imported" // a new draft
	Merged   Status = "merged"   // onto an existing collection
	Existing Status = "existing" // already imported; left as you edited it
	Skipped  Status = "skipped"
	Failed   Status = "failed"
)

// Result is one entry's outcome.
type Result struct {
	Entry    string   `json:"entry"`
	Status   Status   `json:"status"`
	Item     string   `json:"item,omitempty"`
	Slug     string   `json:"slug,omitempty"`
	Visible  bool     `json:"visible"` // not hide: true in content.ts
	Note     string   `json:"note,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// Report is a whole import, saved to <state>/imports/content-ts.json.
type Report struct {
	Source  string   `json:"source"`
	Results []Result `json:"results"`
}

// Importer holds what an import needs.
type Importer struct {
	Working *store.Working
	Ingest  *ingest.Ingester
	// Fetch downloads a thumbnail; nil uses http.Get.
	Fetch func(ctx context.Context, url string) (io.ReadCloser, error)
	IDs   *ids.Generator
}

// Import maps src onto the working manifest in one update.
func (im *Importer) Import(ctx context.Context, src *Source, sourceName string) (Report, error) {
	rep := Report{Source: sourceName}
	m, _, _, err := im.Working.Load()
	if err != nil {
		return rep, err
	}
	have := slugIndex(m)

	// Thumbnails first (downloads and ingest can't run inside the update).
	thumbs := map[string]manifest.AssetRef{}
	thumbErr := map[string]string{}
	for _, e := range src.Content {
		if e.Thumbnail == "" || skipped[e.ID] || have[slugOf(e)] {
			continue
		}
		a, err := im.thumbnail(ctx, e.Thumbnail)
		if err != nil {
			thumbErr[e.ID] = err.Error()
			continue
		}
		thumbs[e.ID] = a
	}

	_, err = im.Working.Update(func(m *manifest.Manifest) error {
		rep.Results = nil
		have := slugIndex(m)
		im.taxonomy(m, src)
		im.profile(m, src.Profile)
		im.owned(m, src)
		for _, s := range declared {
			if !slices.ContainsFunc(m.Surfaces, func(x manifest.Surface) bool { return x.ID == s.ID }) {
				m.Surfaces = append(m.Surfaces, s)
			}
		}
		order := orders(src)
		for _, e := range src.Content {
			r := Result{Entry: e.ID, Visible: !e.Hide}
			switch {
			case skipped[e.ID]:
				r.Status, r.Note = Skipped, "app chrome (About, Contact, Settings stay in Nickel Tools)"
			case collections[e.ID] != "":
				im.mergeCollection(m, e, order, src, &r)
			case have[slugOf(e)]:
				r.Status, r.Slug, r.Note = Existing, slugOf(e), "already imported; your edits stay"
				for i := range m.Items {
					if string(m.Items[i].Slug) == slugOf(e) {
						r.Item = string(m.Items[i].ID)
					}
				}
			default:
				it := im.item(e, order, src, &r)
				if a, ok := thumbs[e.ID]; ok {
					it.Assets = append(it.Assets, a)
				} else if msg := thumbErr[e.ID]; msg != "" {
					r.Warnings = append(r.Warnings, "thumbnail not imported: "+msg)
				}
				m.Items = append(m.Items, it)
				r.Status, r.Item, r.Slug = Imported, string(it.ID), string(it.Slug)
			}
			rep.Results = append(rep.Results, r)
		}
		return nil
	})
	return rep, err
}

func slugOf(e Entry) string {
	if s, ok := renamed[e.ID]; ok {
		return s
	}
	return e.ID
}

func slugIndex(m *manifest.Manifest) map[string]bool {
	out := map[string]bool{}
	for _, it := range m.Items {
		out[string(it.Slug)] = true
		for _, a := range it.Aliases {
			out[string(a)] = true
		}
	}
	return out
}

func (im *Importer) newID() manifest.Ulid {
	if im.IDs != nil {
		return manifest.Ulid(im.IDs.New().String())
	}
	return manifest.Ulid(ids.New().String())
}

// item maps one entry onto a new draft.
func (im *Importer) item(e Entry, order map[string]map[string]int, src *Source, r *Result) manifest.Item {
	it := manifest.Item{
		ID: im.newID(), Slug: manifest.Slug(slugOf(e)), Kind: manifest.KindProject,
		Visibility: manifest.ItemVisibilityDraft, Title: e.Title,
	}
	if slugOf(e) != e.ID {
		it.Aliases = []manifest.Slug{manifest.Slug(e.ID)}
	}
	if site, ok := experiences[e.ID]; ok {
		it.Kind = manifest.KindExperience
		if site != "" {
			it.Facts = manifest.ItemFacts{"site": site}
		}
	}
	im.details(&it, e, r)
	it.Placements = im.placements(e, order, src, r)
	it.Copy = im.copy(e, r)
	it.Private = notes(e)
	return it
}

// details sets the fields every entry shares.
func (im *Importer) details(it *manifest.Item, e Entry, r *Result) {
	if s := strings.TrimSpace(e.Description); s != "" {
		it.Summary = &s
	}
	if s := strings.TrimSpace(e.Why); s != "" {
		it.Why = &s
	}
	if d := date(e.Date); d != nil {
		it.Date = d
	} else if e.Date != "" {
		r.Warnings = append(r.Warnings, fmt.Sprintf("date %q isn't a date; left out", e.Date))
	}
	st := e.Status
	if st == "" {
		st = "operational" // content.ts's default
	}
	s := manifest.Slug(st)
	it.Status = &s
	if e.Category != "" {
		c := manifest.Slug(slug(category(e.Category)))
		it.Category = &c
	}
	it.Tech, it.Tags, it.Mediums = slugs(e.Tech), slugs(e.Tags), slugs(e.Mediums)
	it.Links = links(e, r)
}

func links(e Entry, r *Result) []manifest.Link {
	var out []manifest.Link
	add := func(rel, u string) {
		if u == "" {
			return
		}
		out = append(out, manifest.Link{Rel: manifest.LinkRel(rel), URL: manifest.HttpURL(rewrite(u))})
	}
	switch {
	case e.URL != "" && isRepo(e.URL) && e.Source == "":
		add("source", e.URL)
	case e.URL != "":
		add("live", e.URL)
	case e.Route != "" && e.Route != "#":
		add("live", NickelToolsBase+e.Route)
	}
	if e.Source != "" && e.Source != e.URL {
		add("source", e.Source)
	}
	add("backend-source", e.BackendSource)
	return out
}

func isRepo(u string) bool { return strings.HasPrefix(u, "https://github.com/") }

func rewrite(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return u
	}
	if to, ok := urlRewrites[p.Hostname()]; ok {
		p.Host = to
		return p.String()
	}
	return u
}

// placements turn surfaces and the *Only settings into placements. Order is
// content.ts's array order per surface (the pages render it so); the
// showroom shows the software page's entries, newest first.
func (im *Importer) placements(e Entry, order map[string]map[string]int, src *Source, r *Result) []manifest.Placement {
	var out []manifest.Placement
	featured := slices.Index(src.Featured, e.ID)
	for _, s := range e.Surfaces {
		id := surfaceFor[s]
		if id == "" {
			r.Warnings = append(r.Warnings, fmt.Sprintf("surface %q isn't one Canon knows", s))
			continue
		}
		o := order[id][e.ID]
		pl := manifest.Placement{Surface: manifest.Slug(id), Order: &o, Config: manifest.PlacementConfig{}}
		switch s {
		case "desktop":
			im.desktop(&pl, e, featured >= 0)
		case "software", "design":
			if pv := preview(e, r); pv != nil {
				pl.Config["preview"] = pv
			}
		case "art":
			section := "primary"
			if e.PortfolioOnly != nil && e.PortfolioOnly.ArtGrouping == "collection" {
				section = "collections"
			}
			pl.Config["section"] = section
		}
		out = append(out, pl)
		if s == "software" {
			out = append(out, manifest.Placement{Surface: "holodeck-showroom", Config: manifest.PlacementConfig{}})
		}
	}
	return out
}

func (im *Importer) desktop(pl *manifest.Placement, e Entry, pinned bool) {
	d := e.DesktopOnly
	// Internal apps open in a window; links in a new tab unless the entry
	// says otherwise (content.ts's default).
	open := "new-tab"
	if e.Route != "" {
		open = "in-window"
		if e.Route != "#" {
			pl.Config["route"] = e.Route
		}
	} else if d != nil && d.OpenInNewTab != nil && !*d.OpenInNewTab {
		open = "in-window"
	}
	pl.Config["open"] = open
	if d != nil {
		w := map[string]any{}
		if d.WindowWidth > 0 {
			w["width"] = d.WindowWidth
		}
		if d.WindowHeight > 0 {
			w["height"] = d.WindowHeight
		}
		if len(w) > 0 {
			pl.Config["window"] = w
		}
		if d.ContentfulDescription != nil {
			// BatchAnalyzerDescription -> batch-analyzer; the component
			// stays in Nickel Tools, mapped by this key.
			pl.Config["component"] = componentSlug(strings.TrimSuffix(d.ContentfulDescription.Ident, "Description"))
		}
		if t := strings.TrimSpace(d.Title); t != "" {
			pl.Copy = &manifest.CopyFields{Title: &t}
		}
	}
	if pinned {
		pl.Config["pinned"] = true
	}
}

// preview mirrors the software page's getProjectLayout: "none", phone
// screens, the laser room for 3D worlds, otherwise the page in a desktop
// frame. Framed pages must be on owned domains, so anything else falls back
// to no preview, with a warning.
func preview(e Entry, r *Result) map[string]any {
	var screens []string
	if e.PortfolioOnly != nil && len(e.PortfolioOnly.Preview) > 0 {
		var none string
		if json.Unmarshal(e.PortfolioOnly.Preview, &none) == nil && none == "none" {
			return map[string]any{"kind": "none"}
		}
		var ms struct {
			MobileScreens []string `json:"mobileScreens"`
		}
		if json.Unmarshal(e.PortfolioOnly.Preview, &ms) == nil {
			screens = ms.MobileScreens
		}
	}
	kind, urls := "", []string{}
	switch {
	case len(screens) > 0:
		kind = "phones"
		for _, s := range screens {
			urls = append(urls, rewrite(s))
		}
	case e.URL != "" && (strings.Contains(e.URL, "nicolebelovoskey.com") || strings.Contains(e.URL, "sphere.saucedog.art")):
		kind, urls = "laser-room", []string{e.URL}
	case e.Route != "" && e.Route != "#":
		kind, urls = "desktop", []string{NickelToolsBase + e.Route}
	case e.URL != "":
		kind, urls = "desktop", []string{rewrite(e.URL)}
	default:
		return map[string]any{"kind": "none"}
	}
	for _, u := range urls {
		if !manifest.Owned(ownedList(), u) && !ownedExact(u) {
			r.Warnings = append(r.Warnings, fmt.Sprintf("preview frames %s, which isn't on an owned domain; no preview until it is", u))
			return map[string]any{"kind": "none"}
		}
	}
	list := make([]any, len(urls))
	for i, u := range urls {
		list[i] = u
	}
	return map[string]any{"kind": kind, "urls": list}
}

func ownedList() []string { return ownedDomains }

// ownedExact: netlify.app and github.io hosts count as yours (they're added
// to ownedDomains exactly; see owned).
func ownedExact(u string) bool {
	p, err := url.Parse(u)
	return err == nil && sharedHost(p.Hostname())
}

func sharedHost(h string) bool {
	return strings.HasSuffix(h, ".netlify.app") || h == "nick5616.github.io"
}

// copy turns *Override fields into per-surface copy. Only title, summary,
// why and date can differ per surface; anything else is reported and the
// item keeps its shared value.
func (im *Importer) copy(e Entry, r *Result) manifest.ItemCopy {
	out := manifest.ItemCopy{}
	keys := make([]string, 0, len(e.Overrides))
	for k := range e.Overrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, s := range keys {
		id := surfaceFor[s]
		var cf manifest.CopyFields
		for field, raw := range e.Overrides[s] {
			var v string
			if err := json.Unmarshal(raw, &v); err != nil || id == "" {
				r.Warnings = append(r.Warnings, fmt.Sprintf("%sOverride.%s can't differ per surface in Canon; the shared value is kept", s, field))
				continue
			}
			v = strings.TrimSpace(v)
			switch field {
			case "title":
				cf.Title = &v
			case "description":
				cf.Summary = &v
			case "why":
				cf.Why = &v
			case "date":
				d := date(v)
				label := v
				if d != nil {
					label = displayLabel(*d)
				}
				cf.Date = &manifest.CopyFieldsDate{Label: label}
			default:
				r.Warnings = append(r.Warnings, fmt.Sprintf("%sOverride.%s can't differ per surface in Canon; the shared value is kept", s, field))
			}
		}
		if cf != (manifest.CopyFields{}) {
			out[id] = cf
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func displayLabel(d manifest.Date) string {
	if d.Label != nil {
		return *d.Label
	}
	return string(d.Value)
}

// notes carries content.ts's comments, and where the item came from.
func notes(e Entry) *manifest.Private {
	n := []string{"Imported from content.ts entry " + e.ID + "."}
	if e.Hide {
		n = append(n, "Hidden in content.ts (hide: true).")
	}
	if len(e.Comments) > 0 {
		n = append(n, strings.Join(e.Comments, " "))
	}
	s := strings.Join(n, " ")
	return &manifest.Private{Notes: &s}
}

// mergeCollection puts an art entry onto the hall the bucket import made:
// its words (where the hall has none), its pages, and its old id as an
// alias. A second run changes nothing.
func (im *Importer) mergeCollection(m *manifest.Manifest, e Entry, order map[string]map[string]int, src *Source, r *Result) {
	target := collections[e.ID]
	var it *manifest.Item
	for i := range m.Items {
		if string(m.Items[i].Slug) == target && m.Items[i].Kind == manifest.KindCollection {
			it = &m.Items[i]
		}
	}
	if it == nil {
		r.Status, r.Note = Skipped, "no "+target+" collection to merge into (run canon import bucket first)"
		return
	}
	r.Item, r.Slug = string(it.ID), target
	if slices.Contains(it.Aliases, manifest.Slug(e.ID)) {
		r.Status, r.Note = Existing, "already merged into "+target
		return
	}
	it.Aliases = append(it.Aliases, manifest.Slug(e.ID))
	if it.Summary == nil {
		cp := e.Description
		if d, ok := e.Overrides["art"]["description"]; ok {
			json.Unmarshal(d, &cp)
		}
		if cp = strings.TrimSpace(cp); cp != "" {
			it.Summary = &cp
		}
	}
	if it.Why == nil && strings.TrimSpace(e.Why) != "" {
		w := strings.TrimSpace(e.Why)
		it.Why = &w
	}
	if len(it.Mediums) == 0 {
		it.Mediums = slugs(e.Mediums)
	}
	for _, pl := range im.placements(e, order, src, r) {
		if !slices.ContainsFunc(it.Placements, func(x manifest.Placement) bool { return x.Surface == pl.Surface }) {
			it.Placements = append(it.Placements, pl)
		}
	}
	r.Status, r.Note = Merged, "merged into the "+target+" collection"
}

// orders numbers each surface's entries in content.ts order (10, 20, ...),
// then applies rankAfter.
func orders(src *Source) map[string]map[string]int {
	out := map[string]map[string]int{}
	for _, e := range src.Content {
		for _, s := range e.Surfaces {
			id := surfaceFor[s]
			if out[id] == nil {
				out[id] = map[string]int{}
			}
			out[id][e.ID] = (len(out[id]) + 1) * 10
		}
	}
	for a, b := range rankAfter {
		for _, o := range out {
			if oa, ok := o[a]; ok {
				if ob, ok := o[b]; ok && ob > oa {
					o[b] = oa - 1
				}
			}
		}
	}
	return out
}

// taxonomy adds content.ts's vocabularies (merging categories per D59).
// Existing terms keep their labels.
func (im *Importer) taxonomy(m *manifest.Manifest, src *Source) {
	add := func(terms *[]manifest.TaxonomyTerm, labels []string, icon func(string) string) {
		for _, l := range labels {
			s := manifest.Slug(slug(l))
			if slices.ContainsFunc(*terms, func(t manifest.TaxonomyTerm) bool { return t.Slug == s }) {
				continue
			}
			t := manifest.TaxonomyTerm{Slug: s, Label: l}
			if icon != nil {
				if ic := icon(l); ic != "" {
					t.Icon = &ic
				}
			}
			*terms = append(*terms, t)
		}
	}
	var cats []string
	for _, c := range src.Taxonomy["CATEGORIES"] {
		if mc := category(c); !slices.Contains(cats, mc) {
			cats = append(cats, mc)
		}
	}
	add(&m.Taxonomy.Categories, cats, func(l string) string {
		if ic, ok := src.Icons[l]; ok {
			return ic
		}
		for from, to := range categoryMerges {
			if to == l {
				if ic, ok := src.Icons[from]; ok {
					return ic
				}
			}
		}
		return ""
	})
	add(&m.Taxonomy.Tech, src.Taxonomy["TECHNOLOGIES"], nil)
	add(&m.Taxonomy.Mediums, src.Taxonomy["MEDIUMS"], nil)
	add(&m.Taxonomy.Tags, src.Taxonomy["TAGS"], nil)
}

func category(label string) string {
	if to, ok := categoryMerges[label]; ok {
		return to
	}
	return label
}

// profile fills the profile's empty fields from profile.ts.
func (im *Importer) profile(m *manifest.Manifest, p *Profile) {
	if p == nil {
		return
	}
	pr := &m.Profile
	if pr.Name == "" {
		pr.Name = p.Name
	}
	if pr.Headline == nil && p.Headline != "" {
		pr.Headline = &p.Headline
	}
	if pr.Summary == nil && p.Summary != "" {
		pr.Summary = &p.Summary
	}
	if pr.Links == nil && len(p.Links) > 0 {
		l := &manifest.ProfileLinks{}
		if v := p.Links["github"]; v != "" {
			l.Github = httpURL(v)
		}
		if v := p.Links["linkedin"]; v != "" {
			l.Linkedin = httpURL(v)
		}
		if v := p.Links["email"]; v != "" {
			l.Email = &v
		}
		if v := p.Links["website"]; v != "" {
			l.Website = httpURL(v)
		}
		pr.Links = l
	}
}

// owned adds D59's domains and the shared-platform hosts content links to.
func (im *Importer) owned(m *manifest.Manifest, src *Source) {
	want := slices.Clone(ownedDomains)
	for _, e := range src.Content {
		for _, u := range []string{e.URL, e.Source} {
			if p, err := url.Parse(rewrite(u)); err == nil && sharedHost(p.Hostname()) {
				want = append(want, p.Hostname())
			}
		}
	}
	for _, d := range want {
		if !slices.Contains(m.OwnedDomains, d) {
			m.OwnedDomains = append(m.OwnedDomains, d)
		}
	}
}

// date reads content.ts dates: YYYY-MM or YYYY-MM-DD as is; free text
// ("Sometime in like Nov 2024") becomes its month or year, the text kept
// as the label (the bucket import's parser).
func date(s string) *manifest.Date {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if isoDate.MatchString(s) {
		return &manifest.Date{Value: manifest.IsoPartialDate(s)}
	}
	v, label, ok := importbucket.ParseDate(s)
	if !ok {
		return nil
	}
	d := &manifest.Date{Value: manifest.IsoPartialDate(v)}
	if label != "" {
		d.Label = &label
	}
	return d
}

var (
	isoDate = regexp.MustCompile(`^\d{4}(-\d{2}(-\d{2})?)?$`)
	nonSlug = regexp.MustCompile(`[^a-z0-9]+`)
)

// slug makes a taxonomy slug from a label: "AI / Productivity" ->
// ai-productivity, "C#" -> csharp, "C++" -> cpp, "Next.js" -> next-js.
func slug(label string) string {
	s := strings.NewReplacer("c#", "csharp", "c++", "cpp", "f#", "fsharp", ".net", "dotnet").Replace(strings.ToLower(label))
	return strings.Trim(nonSlug.ReplaceAllString(s, "-"), "-")
}

// componentSlug makes a component key from a React component name:
// BatchAnalyzer -> batch-analyzer.
func componentSlug(name string) string {
	var b strings.Builder
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('-')
		}
		b.WriteRune(r)
	}
	return slug(b.String())
}

func slugs(labels []string) []manifest.Slug {
	var out []manifest.Slug
	for _, l := range labels {
		out = append(out, manifest.Slug(slug(l)))
	}
	return out
}

// thumbnail downloads a content.ts thumbnail (a path on nickeltools.dev)
// and ingests it as a blob only.
func (im *Importer) thumbnail(ctx context.Context, path string) (manifest.AssetRef, error) {
	u := path
	if strings.HasPrefix(path, "/") {
		u = NickelToolsBase + path
	}
	fetch := im.Fetch
	if fetch == nil {
		fetch = func(ctx context.Context, u string) (io.ReadCloser, error) {
			req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
			if err != nil {
				return nil, err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return nil, err
			}
			if resp.StatusCode != 200 {
				resp.Body.Close()
				return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
			}
			return resp.Body, nil
		}
	}
	body, err := fetch(ctx, u)
	if err != nil {
		return manifest.AssetRef{}, err
	}
	defer body.Close()
	o, err := im.Ingest.StageReader(body, filepath.Base(path))
	if err != nil {
		return manifest.AssetRef{}, err
	}
	orig := manifest.BlobRef("sha256:" + o.SHA256())
	fr := im.Ingest.Process(ctx, o, ingest.Options{BlobOnly: true}, nil)
	if fr.Stage == ingest.Failed {
		return manifest.AssetRef{}, fmt.Errorf("%s", deref(fr.Error))
	}
	return manifest.AssetRef{Role: manifest.AssetRoleThumbnail, Blob: manifest.BlobRef(deref(fr.Blob)), Original: &orig}, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
