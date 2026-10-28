package manifest

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/nick5616/canon/engine/surfaces"
	"github.com/nick5616/canon/schema"
)

// Problem codes for the semantic rules. Schema failures use "schema".
const (
	CodeSchema                 = "schema"
	CodeDuplicateID            = "duplicate-id"
	CodeDuplicateSlug          = "duplicate-slug"
	CodeAliasCollision         = "alias-collision"
	CodeDuplicateTaxonomy      = "duplicate-taxonomy"
	CodeUnknownTaxonomy        = "unknown-taxonomy"
	CodeDuplicateSurface       = "duplicate-surface"
	CodeUnregisteredSurface    = "unregistered-surface"
	CodeUnknownSurface         = "unknown-surface"
	CodePlacementConfig        = "placement-config"
	CodePlacementKey           = "placement-key"
	CodeMemberMissing          = "member-missing"
	CodeMemberSelf             = "member-self"
	CodeMemberCycle            = "member-cycle"
	CodeNotOwnedDomain         = "not-owned-domain"
	CodeLinkNotOwned           = "link-not-owned"
	CodeSharedPlatformWildcard = "shared-platform-wildcard"
	CodeBadDate                = "bad-date"
)

// sharedPlatforms are suffixes where anyone can register a subdomain, so a
// wildcard on them would let strangers pass the owned-domain check (D10).
var sharedPlatforms = []string{
	"netlify.app", "github.io", "vercel.app", "pages.dev", "web.app", "firebaseapp.com",
	"herokuapp.com", "onrender.com", "fly.dev", "workers.dev", "glitch.me", "surge.sh",
	"appspot.com", "run.app", "storage.googleapis.com", "gitlab.io", "neocities.org",
}

// Validate checks m against the manifest schema and every semantic rule.
func Validate(m *Manifest, reg surfaces.Registry) []Problem {
	b, err := json.Marshal(normalized(m))
	if err != nil {
		return []Problem{{Code: CodeSchema, Severity: Error, Message: err.Error()}}
	}
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		return []Problem{{Code: CodeSchema, Severity: Error, Message: err.Error()}}
	}
	if ps := SchemaProblems(schema.Manifest, doc); len(ps) > 0 {
		return ps
	}
	return semantic(m, reg)
}

type checker struct {
	m     *Manifest
	reg   surfaces.Registry
	out   []Problem
	owned []string
}

func (c *checker) add(sev Severity, code, path, format string, args ...any) {
	c.out = append(c.out, Problem{Code: code, Severity: sev, Path: path, Message: fmt.Sprintf(format, args...)})
}

func semantic(m *Manifest, reg surfaces.Registry) []Problem {
	c := &checker{m: m, reg: reg, owned: m.OwnedDomains}
	c.domains()
	tax := c.taxonomy()
	declared := c.surfaces()
	c.identity()
	c.dates()

	byID := map[Ulid]int{}
	for i, it := range m.Items {
		byID[it.ID] = i
	}
	for i := range m.Items {
		c.item(i, tax, declared, byID)
	}
	c.cycles(byID)
	return c.out
}

func (c *checker) domains() {
	for i, d := range c.m.OwnedDomains {
		rest, ok := strings.CutPrefix(d, "*.")
		if !ok {
			continue
		}
		if slices.Contains(sharedPlatforms, rest) {
			c.add(Error, CodeSharedPlatformWildcard, fmt.Sprintf("/ownedDomains/%d", i),
				"%s would match anyone's site on %s; list exact hosts instead", d, rest)
		}
	}
}

// taxonomy returns the declared slugs per vocabulary.
func (c *checker) taxonomy() map[string]map[Slug]bool {
	t := c.m.Taxonomy
	out := map[string]map[Slug]bool{}
	vocab := func(name string, slugs []Slug) {
		out[name] = map[Slug]bool{}
		for i, s := range slugs {
			if out[name][s] {
				c.add(Error, CodeDuplicateTaxonomy, fmt.Sprintf("/taxonomy/%s/%d", name, i), "%s %q is declared twice", name, s)
			}
			out[name][s] = true
		}
	}
	terms := func(ts []TaxonomyTerm) []Slug {
		s := make([]Slug, len(ts))
		for i, t := range ts {
			s[i] = t.Slug
		}
		return s
	}
	vocab("categories", terms(t.Categories))
	vocab("tech", terms(t.Tech))
	vocab("mediums", terms(t.Mediums))
	vocab("tags", terms(t.Tags))
	vp := make([]Slug, len(t.Viewports))
	for i, v := range t.Viewports {
		vp[i] = v.Slug
	}
	vocab("viewports", vp)
	return out
}

// surfaces checks the surfaces table and returns the declared ids.
func (c *checker) surfaces() map[Slug]bool {
	declared := map[Slug]bool{}
	for i, s := range c.m.Surfaces {
		p := fmt.Sprintf("/surfaces/%d", i)
		if declared[s.ID] {
			c.add(Error, CodeDuplicateSurface, p, "surface %q is declared twice", s.ID)
		}
		declared[s.ID] = true
		if _, ok := c.reg.Get(string(s.ID)); !ok {
			c.add(Error, CodeUnregisteredSurface, p, "surface %q has no registered schemas (known: %s)",
				s.ID, strings.Join(c.reg.IDs(), ", "))
		}
	}
	return declared
}

// identity checks ids, slugs and aliases are unique across all items.
func (c *checker) identity() {
	ids := map[Ulid]int{}
	names := map[Slug]string{} // slug or alias -> who owns it
	for i, it := range c.m.Items {
		if j, ok := ids[it.ID]; ok {
			c.add(Error, CodeDuplicateID, fmt.Sprintf("/items/%d/id", i), "id %s is also item %d", it.ID, j)
		}
		ids[it.ID] = i
	}
	for i, it := range c.m.Items {
		if owner, ok := names[it.Slug]; ok {
			c.add(Error, CodeDuplicateSlug, fmt.Sprintf("/items/%d/slug", i), "slug %q is already %s", it.Slug, owner)
		} else {
			names[it.Slug] = fmt.Sprintf("the slug of item %d", i)
		}
	}
	for i, it := range c.m.Items {
		for j, a := range it.Aliases {
			if owner, ok := names[a]; ok {
				c.add(Error, CodeAliasCollision, fmt.Sprintf("/items/%d/aliases/%d", i, j), "alias %q is already %s", a, owner)
				continue
			}
			names[a] = fmt.Sprintf("an alias of item %d", i)
		}
	}
}

func (c *checker) dates() {
	for i, it := range c.m.Items {
		if it.Date != nil {
			c.date(fmt.Sprintf("/items/%d/date/value", i), it.Date.Value)
		}
	}
	for i, e := range c.m.Profile.Experience {
		c.date(fmt.Sprintf("/profile/experience/%d/start", i), e.Start)
		if e.End != nil {
			c.date(fmt.Sprintf("/profile/experience/%d/end", i), *e.End)
		}
	}
	for i, e := range c.m.Profile.Education {
		if e.Start != nil {
			c.date(fmt.Sprintf("/profile/education/%d/start", i), *e.Start)
		}
		if e.End != nil {
			c.date(fmt.Sprintf("/profile/education/%d/end", i), *e.End)
		}
	}
}

// date checks a schema-valid ISO partial is a real calendar date (no Feb 30).
func (c *checker) date(path string, d IsoPartialDate) {
	if _, err := ParseDate(string(d)); err != nil {
		c.add(Error, CodeBadDate, path, "%v", err)
	}
}

// ParseDate parses an ISO partial (2025, 2025-10, 2025-10-03) to the first
// instant it covers.
func ParseDate(s string) (time.Time, error) {
	layout := map[int]string{4: "2006", 7: "2006-01", 10: "2006-01-02"}[len(s)]
	if layout == "" {
		return time.Time{}, fmt.Errorf("%q is not YYYY, YYYY-MM or YYYY-MM-DD", s)
	}
	t, err := time.Parse(layout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not a real date", s)
	}
	return t, nil
}

func (c *checker) item(i int, tax map[string]map[Slug]bool, declared map[Slug]bool, byID map[Ulid]int) {
	it := c.m.Items[i]
	p := fmt.Sprintf("/items/%d", i)

	ref := func(vocab string, s Slug, path string) {
		if !tax[vocab][s] {
			c.add(Error, CodeUnknownTaxonomy, path, "%q is not in taxonomy.%s", s, vocab)
		}
	}
	if it.Category != nil {
		ref("categories", *it.Category, p+"/category")
	}
	for j, s := range it.Tech {
		ref("tech", s, fmt.Sprintf("%s/tech/%d", p, j))
	}
	for j, s := range it.Tags {
		ref("tags", s, fmt.Sprintf("%s/tags/%d", p, j))
	}
	for j, s := range it.Mediums {
		ref("mediums", s, fmt.Sprintf("%s/mediums/%d", p, j))
	}
	for j, a := range it.Assets {
		if a.Viewport != nil {
			ref("viewports", *a.Viewport, fmt.Sprintf("%s/assets/%d/viewport", p, j))
		}
	}

	for surface := range it.Copy {
		if !declared[Slug(surface)] {
			c.add(Error, CodeUnknownSurface, p+"/copy/"+surface, "copy for undeclared surface %q", surface)
		}
	}

	// Placements.
	keys := map[Slug]map[string]int{} // surface -> key -> count
	counts := map[Slug]int{}
	for _, pl := range it.Placements {
		counts[pl.Surface]++
	}
	for j, pl := range it.Placements {
		pp := fmt.Sprintf("%s/placements/%d", p, j)
		if !declared[pl.Surface] {
			c.add(Error, CodeUnknownSurface, pp+"/surface", "surface %q is not in the surfaces table", pl.Surface)
		}
		if s, ok := c.reg.Get(string(pl.Surface)); ok {
			var cfg any = map[string]any(pl.Config)
			if pl.Config == nil {
				cfg = map[string]any{}
			}
			for _, sp := range schemaProblems(s.Placement, roundTrip(cfg), pp+"/config") {
				sp.Code = CodePlacementConfig
				c.out = append(c.out, sp)
			}
			if vp, ok := pl.Config["viewport"].(string); ok && pl.Surface == "holodeck-showroom" {
				ref("viewports", Slug(vp), pp+"/config/viewport")
			}
		}
		if counts[pl.Surface] > 1 {
			k := ""
			if pl.Key != nil {
				k = string(*pl.Key)
			}
			if k == "" {
				c.add(Error, CodePlacementKey, pp, "item has %d placements on %s; each needs a key", counts[pl.Surface], pl.Surface)
			} else {
				if keys[pl.Surface] == nil {
					keys[pl.Surface] = map[string]int{}
				}
				if _, dup := keys[pl.Surface][k]; dup {
					c.add(Error, CodePlacementKey, pp+"/key", "key %q is used twice on %s", k, pl.Surface)
				}
				keys[pl.Surface][k] = j
			}
		}
		c.previewURLs(pp, pl)
	}

	// Members.
	for j, mid := range it.Members {
		mp := fmt.Sprintf("%s/members/%d", p, j)
		switch {
		case mid == it.ID:
			c.add(Error, CodeMemberSelf, mp, "a collection can't contain itself")
		default:
			if _, ok := byID[mid]; !ok {
				c.add(Error, CodeMemberMissing, mp, "no item with id %s", mid)
			}
		}
	}

	// Links.
	embeddable, _ := it.Facts["embeddable"].(bool)
	for j, l := range it.Links {
		lp := fmt.Sprintf("%s/links/%d/url", p, j)
		if c.isOwned(string(l.URL)) {
			continue
		}
		if embeddable && l.Rel == LinkRelLive {
			c.add(Error, CodeNotOwnedDomain, lp, "%s is framed (embeddable live link) but its host is not in ownedDomains", l.URL)
		} else {
			c.add(Warning, CodeLinkNotOwned, lp, "%s is not on an owned domain", l.URL)
		}
	}
}

// previewURLs requires every framed preview URL to be on an owned domain.
func (c *checker) previewURLs(pp string, pl Placement) {
	preview, ok := pl.Config["preview"].(map[string]any)
	if !ok {
		return
	}
	urls, _ := preview["urls"].([]any)
	for k, u := range urls {
		s, _ := u.(string)
		if !c.isOwned(s) {
			c.add(Error, CodeNotOwnedDomain, fmt.Sprintf("%s/config/preview/urls/%d", pp, k),
				"%s would be framed but its host is not in ownedDomains", s)
		}
	}
}

// isOwned matches a URL's host against ownedDomains: exact hosts, or
// *.domain for any subdomain (not the apex).
func (c *checker) isOwned(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	for _, d := range c.owned {
		if rest, ok := strings.CutPrefix(d, "*."); ok {
			if strings.HasSuffix(host, "."+rest) {
				return true
			}
		} else if host == d {
			return true
		}
	}
	return false
}

// cycles reports collections that contain themselves through other
// collections.
func (c *checker) cycles(byID map[Ulid]int) {
	const (
		white = iota
		grey
		black
	)
	color := map[Ulid]int{}
	var visit func(id Ulid, path []Ulid)
	visit = func(id Ulid, path []Ulid) {
		color[id] = grey
		i := byID[id]
		for _, mid := range c.m.Items[i].Members {
			j, ok := byID[mid]
			if !ok || mid == id {
				continue // reported as member-missing / member-self
			}
			if c.m.Items[j].Kind != KindCollection {
				continue
			}
			switch color[mid] {
			case grey:
				cyc := append(append([]Ulid{}, path...), id, mid)
				names := make([]string, len(cyc))
				for k, x := range cyc {
					names[k] = string(c.m.Items[byID[x]].Slug)
				}
				c.add(Error, CodeMemberCycle, fmt.Sprintf("/items/%d/members", i), "collections form a cycle: %s", strings.Join(names, " -> "))
			case white:
				visit(mid, append(path, id))
			}
		}
		color[id] = black
	}
	for _, it := range c.m.Items {
		if it.Kind == KindCollection && color[it.ID] == white {
			visit(it.ID, nil)
		}
	}
}

// roundTrip turns a Go value into the generic JSON form the validator wants.
func roundTrip(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if json.Unmarshal(b, &out) != nil {
		return v
	}
	return out
}
