// Package views resolves the public projection into one flat, final view
// per surface (design doc, "Surface views"; M0-M1 plan §2.4): copy merged,
// assets as URLs, order applied, hidden placements dropped. Resolution is
// pure: derived outputs come in through Media, and every file a view links
// is reported so publish can copy exactly those (D51).
package views

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/nick5616/canon/engine/canonjson"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/surfaces"
)

// PipelineVersion goes in every view. Bump the minor version when views'
// content changes for the same manifest.
const PipelineVersion = "0.5.0"

// Size is one image size in the private bucket.
type Size struct {
	Path          string // object name, e.g. derived/<jobkey>/w800.webp
	Width, Height int
}

// Image is an image blob's derived sizes, smallest first, and its tiny
// placeholder.
type Image struct {
	Sizes []Size
	LQIP  []byte // WebP bytes; nil when lqip hasn't run
}

// Track is a track blob's playable files.
type Track struct {
	Audio    string // object name: dj-mp3 output, or the source MP3 (D49)
	Analysis string // object name of djgrid's analysis.json
}

// Media looks up derived outputs by blob ("sha256:<hex>"). ok is false
// while the jobs haven't finished, here or in the bucket.
type Media interface {
	Image(blob string) (Image, bool)
	Poster(blob string) (Image, bool)
	Track(blob string) (Track, bool)
}

// Result is one resolved view.
type Result struct {
	Surface string
	View    any
	// Files are the object names the view links, to be published.
	Files map[string]bool
	// Problems are warnings: entries left out because a file isn't ready.
	Problems []manifest.Problem
	// Images maps item id to its main image URL (for import redirects).
	Images map[string]string
}

// Bytes encodes the view canonically and checks it against its schema.
func (r Result) Bytes(reg surfaces.Registry) ([]byte, error) {
	b, err := canonjson.Marshal(r.View)
	if err != nil {
		return nil, err
	}
	s, ok := reg.Get(r.Surface)
	if !ok {
		return nil, fmt.Errorf("views: unknown surface %s", r.Surface)
	}
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	if err := s.View.Validate(doc); err != nil {
		return nil, fmt.Errorf("view %s fails its schema: %v", r.Surface, err)
	}
	return b, nil
}

// Resolve builds surface's view from m, which must already be the public
// projection (views never see drafts). base is the public URL prefix.
func Resolve(m *manifest.Manifest, surface string, media Media, base string) (Result, error) {
	f, ok := resolvers[surface]
	if !ok {
		return Result{}, fmt.Errorf("views: no resolver for %s", surface)
	}
	c := &ctx{m: m, surface: surface, media: media, base: strings.TrimSuffix(base, "/"),
		res: Result{Surface: surface, Files: map[string]bool{}, Images: map[string]string{}}, byID: map[manifest.Ulid]*manifest.Item{}}
	for i := range m.Items {
		c.byID[m.Items[i].ID] = &m.Items[i]
	}
	c.res.View = f(c)
	return c.res, nil
}

// Surfaces lists every surface with a resolver, sorted.
func Surfaces() []string {
	out := make([]string, 0, len(resolvers))
	for s := range resolvers {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

type ctx struct {
	m       *manifest.Manifest
	surface string
	media   Media
	base    string
	res     Result
	byID    map[manifest.Ulid]*manifest.Item
	used    used
}

func (c *ctx) warn(code, format string, args ...any) {
	c.res.Problems = append(c.res.Problems, manifest.Problem{Code: code, Severity: manifest.Warning,
		Message: fmt.Sprintf("%s: %s", c.surface, fmt.Sprintf(format, args...))})
}

func (c *ctx) url(path string) string {
	c.res.Files[path] = true
	return c.base + "/" + path
}

// placed is one placement of an item on this surface.
type placed struct {
	it *manifest.Item
	pl manifest.Placement
}

// placements returns every visible placement on the surface, in manifest
// order (entries are sorted later).
func (c *ctx) placements() []placed {
	var out []placed
	for i := range c.m.Items {
		it := &c.m.Items[i]
		for _, pl := range it.Placements {
			if string(pl.Surface) == c.surface && (pl.Hidden == nil || !*pl.Hidden) {
				out = append(out, placed{it, pl})
			}
		}
	}
	return out
}

// placementOf returns the item's visible placement on this surface, if any.
func (c *ctx) placementOf(it *manifest.Item) (manifest.Placement, bool, bool) {
	for _, pl := range it.Placements {
		if string(pl.Surface) == c.surface {
			return pl, true, pl.Hidden != nil && *pl.Hidden
		}
	}
	return manifest.Placement{}, false, false
}

// --- The common entry ---------------------------------------------------

// Term is a taxonomy term as views ship it.
type Term struct {
	Slug  string  `json:"slug"`
	Label string  `json:"label"`
	Icon  *string `json:"icon,omitempty"`
}

// Date is a resolved date.
type Date struct {
	Value   string  `json:"value"`
	Label   *string `json:"label"`
	Display string  `json:"display"`
	SortKey int     `json:"sortKey"`
}

// Link is a resolved link.
type Link struct {
	Rel   string  `json:"rel"`
	URL   string  `json:"url"`
	Label *string `json:"label"`
}

// Entry is what every view entry carries.
type Entry struct {
	ID       string   `json:"id"`
	Slug     string   `json:"slug"`
	Key      *string  `json:"key"`
	Kind     string   `json:"kind"`
	Title    string   `json:"title"`
	Summary  string   `json:"summary"`
	Why      string   `json:"why"`
	Date     *Date    `json:"date"`
	Status   *string  `json:"status"`
	Category *Term    `json:"category"`
	Tech     []string `json:"tech"`
	Tags     []string `json:"tags"`
	Mediums  []string `json:"mediums"`
	Links    []Link   `json:"links"`
	Order    *int     `json:"order"`
	Featured bool     `json:"featured"`
}

// Taxonomy holds only the terms a view uses.
type Taxonomy struct {
	Categories []Term `json:"categories"`
	Tech       []Term `json:"tech"`
	Mediums    []Term `json:"mediums"`
	Tags       []Term `json:"tags"`
}

// Header starts every view.
type Header struct {
	View            string   `json:"view"`
	ViewVersion     int      `json:"viewVersion"`
	PipelineVersion string   `json:"pipelineVersion"`
	Taxonomy        Taxonomy `json:"taxonomy"`
}

func (c *ctx) header() Header {
	return Header{View: c.surface, ViewVersion: 1, PipelineVersion: PipelineVersion, Taxonomy: c.taxonomy()}
}

type used struct{ categories, tech, mediums, tags map[string]bool }

func (u *used) add(e Entry) {
	if u.tech == nil {
		u.categories, u.tech, u.mediums, u.tags = map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	}
	if e.Category != nil {
		u.categories[e.Category.Slug] = true
	}
	for _, s := range e.Tech {
		u.tech[s] = true
	}
	for _, s := range e.Mediums {
		u.mediums[s] = true
	}
	for _, s := range e.Tags {
		u.tags[s] = true
	}
}

// taxonomy lists the used terms in the manifest's own order.
func (c *ctx) taxonomy() Taxonomy {
	pick := func(terms []manifest.TaxonomyTerm, use map[string]bool) []Term {
		out := []Term{}
		for _, t := range terms {
			if use[string(t.Slug)] {
				out = append(out, Term{Slug: string(t.Slug), Label: t.Label, Icon: t.Icon})
			}
		}
		return out
	}
	tx := c.m.Taxonomy
	return Taxonomy{Categories: pick(tx.Categories, c.used.categories), Tech: pick(tx.Tech, c.used.tech),
		Mediums: pick(tx.Mediums, c.used.mediums), Tags: pick(tx.Tags, c.used.tags)}
}

// copyFor resolves the copy fields field by field: placement, then the
// item's copy for this surface, then the item.
func (c *ctx) copyFor(it *manifest.Item, pl *manifest.Placement) manifest.CopyFields {
	var layers []*manifest.CopyFields
	if pl != nil && pl.Copy != nil {
		layers = append(layers, pl.Copy)
	}
	if cf, ok := it.Copy[c.surface]; ok {
		layers = append(layers, &cf)
	}
	var out manifest.CopyFields
	for _, l := range layers {
		if out.Title == nil {
			out.Title = l.Title
		}
		if out.Summary == nil {
			out.Summary = l.Summary
		}
		if out.Why == nil {
			out.Why = l.Why
		}
		if out.Date == nil {
			out.Date = l.Date
		}
		if out.RichDescription == nil {
			out.RichDescription = l.RichDescription
		}
	}
	if out.Title == nil {
		out.Title = &it.Title
	}
	if out.Summary == nil {
		out.Summary = it.Summary
	}
	if out.Why == nil {
		out.Why = it.Why
	}
	if out.RichDescription == nil {
		out.RichDescription = it.RichDescription
	}
	return out
}

// entry builds the common fields of an item's entry. pl is nil for items
// shown without a placement of their own (a collection's pieces).
func (c *ctx) entry(it *manifest.Item, pl *manifest.Placement) Entry {
	cp := c.copyFor(it, pl)
	e := Entry{
		ID: string(it.ID), Slug: string(it.Slug), Kind: string(it.Kind), Title: *cp.Title,
		Summary: deref(cp.Summary), Why: deref(cp.Why),
		Tech: slugs(it.Tech), Tags: slugs(it.Tags), Mediums: slugs(it.Mediums), Links: []Link{},
	}
	if it.Date != nil {
		d := ResolveDate(string(it.Date.Value), it.Date.Label)
		if cp.Date != nil {
			l := cp.Date.Label
			d.Label, d.Display = &l, l
		}
		e.Date = &d
	}
	if it.Status != nil {
		s := string(*it.Status)
		e.Status = &s
	}
	if it.Category != nil {
		for _, t := range c.m.Taxonomy.Categories {
			if t.Slug == *it.Category {
				e.Category = &Term{Slug: string(t.Slug), Label: t.Label, Icon: t.Icon}
			}
		}
	}
	for _, l := range it.Links {
		e.Links = append(e.Links, Link{Rel: string(l.Rel), URL: string(l.URL), Label: l.Label})
	}
	if pl != nil {
		if pl.Key != nil {
			k := string(*pl.Key)
			e.Key = &k
		}
		e.Order = pl.Order
		e.Featured = pl.Featured != nil && *pl.Featured
	}
	c.used.add(e)
	return e
}

func slugs(in []manifest.Slug) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = string(s)
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// sortEntries: order ascending (none last), then date descending (none
// last), then id, then key.
func sortEntries[T any](es []T, get func(T) *Entry) {
	sort.SliceStable(es, func(i, j int) bool {
		a, b := get(es[i]), get(es[j])
		if (a.Order == nil) != (b.Order == nil) {
			return a.Order != nil
		}
		if a.Order != nil && *a.Order != *b.Order {
			return *a.Order < *b.Order
		}
		ak, bk := -1, -1
		if a.Date != nil {
			ak = a.Date.SortKey
		}
		if b.Date != nil {
			bk = b.Date.SortKey
		}
		if ak != bk {
			return ak > bk
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return deref(a.Key) < deref(b.Key)
	})
}

var months = [...]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

// ResolveDate precomputes display and sortKey, so no site parses dates:
// 2024 -> "2024" (20240000), 2024-12 -> "Dec 2024", 2024-12-03 ->
// "Dec 3, 2024". A label is shown instead when there is one.
func ResolveDate(value string, label *string) Date {
	var y, mo, d int
	fmt.Sscanf(value, "%d-%d-%d", &y, &mo, &d)
	display := fmt.Sprint(y)
	switch {
	case d > 0:
		display = fmt.Sprintf("%s %d, %d", months[mo-1], d, y)
	case mo > 0:
		display = fmt.Sprintf("%s %d", months[mo-1], y)
	}
	if label != nil && *label != "" {
		display = *label
	}
	return Date{Value: value, Label: label, Display: display, SortKey: y*10000 + mo*100 + d}
}

// --- Assets -------------------------------------------------------------

// Asset is a resolved image.
type Asset struct {
	URL    string  `json:"url"`
	Width  int     `json:"width"`
	Height int     `json:"height"`
	Srcset []Src   `json:"srcset"`
	LQIP   *string `json:"lqip"`
	Alt    *string `json:"alt"`
}

// Src is one srcset entry.
type Src struct {
	URL   string `json:"url"`
	Width int    `json:"width"`
}

func (c *ctx) asset(img Image, alt *string) *Asset {
	a := &Asset{Srcset: []Src{}, Alt: alt}
	for _, s := range img.Sizes {
		a.Srcset = append(a.Srcset, Src{URL: c.url(s.Path), Width: s.Width})
	}
	big := img.Sizes[len(img.Sizes)-1]
	a.URL, a.Width, a.Height = c.base+"/"+big.Path, big.Width, big.Height
	if img.LQIP != nil {
		// Inlined, so the placeholder file itself is never published.
		s := "data:image/webp;base64," + base64.StdEncoding.EncodeToString(img.LQIP)
		a.LQIP = &s
	}
	return a
}

// image resolves the item's first asset with one of roles that has
// finished image outputs. It reports whether the item had such an asset at
// all, so a missing output can be told apart from no image.
func (c *ctx) image(it *manifest.Item, roles ...manifest.AssetRole) (*Asset, bool) {
	had := false
	for _, a := range it.Assets {
		if !hasRole(a.Role, roles) {
			continue
		}
		img, ok := c.media.Image(string(a.Blob))
		if !ok {
			if a.Role != manifest.AssetRoleVideo {
				had = true
			}
			continue
		}
		if len(img.Sizes) == 0 {
			continue
		}
		r := c.asset(img, a.Alt)
		c.res.Images[string(it.ID)] = r.URL
		return r, true
	}
	return nil, had
}

// poster resolves a video item's poster.
func (c *ctx) poster(it *manifest.Item) *Asset {
	for _, a := range it.Assets {
		if a.Role != manifest.AssetRoleVideo {
			continue
		}
		if img, ok := c.media.Poster(string(a.Blob)); ok && len(img.Sizes) > 0 {
			return c.asset(img, a.Alt)
		}
		c.warn("view-asset-pending", "%s's poster isn't ready", it.Slug)
	}
	return nil
}

func hasRole(r manifest.AssetRole, roles []manifest.AssetRole) bool {
	for _, x := range roles {
		if x == r {
			return true
		}
	}
	return false
}

// liveLink is the item's first live link.
func liveLink(it *manifest.Item) string {
	for _, l := range it.Links {
		if l.Rel == "live" {
			return string(l.URL)
		}
	}
	return ""
}

func embeddable(it *manifest.Item) *bool {
	if v, ok := it.Facts["embeddable"].(bool); ok {
		return &v
	}
	return nil
}

func cfgString(pl manifest.Placement, key string) string {
	s, _ := pl.Config[key].(string)
	return s
}

func cfgBool(pl manifest.Placement, key string) bool {
	b, _ := pl.Config[key].(bool)
	return b
}
