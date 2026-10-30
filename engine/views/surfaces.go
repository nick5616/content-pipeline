package views

import (
	"sort"
	"strings"

	"github.com/nick5616/canon/engine/manifest"
)

var resolvers = map[string]func(*ctx) any{
	"nickel-desktop":     desktop,
	"software-portfolio": software,
	"design-portfolio":   design,
	"art-portfolio":      artPortfolio,
	"holodeck-showroom":  showroom,
	"holodeck-art-room":  artRoom,
	"holodeck-dj-room":   djRoom,
}

// --- Previews (software, design, showroom) ---------------------------------

// Preview is a resolved preview.
type Preview struct {
	Kind string   `json:"kind"`
	URLs []string `json:"urls"`
}

// preview resolves a placement's preview. auto frames an embeddable item's
// owned live link; anything that can't be framed falls back to its
// screenshots, then to none. An item marked not embeddable never frames,
// whatever the placement asks.
func (c *ctx) preview(it *manifest.Item, kind string, urls []string) Preview {
	live := liveLink(it)
	owned := live != "" && manifest.Owned(c.m.OwnedDomains, live)
	emb := embeddable(it)
	frame := func(k string) Preview {
		u := urls
		if len(u) == 0 && owned {
			u = []string{live}
		}
		if len(u) == 0 || (emb != nil && !*emb && k != "laser-room") {
			return c.screenshots(it)
		}
		return Preview{Kind: k, URLs: u}
	}
	switch kind {
	case "none":
		return Preview{Kind: "none", URLs: []string{}}
	case "", "auto":
		if emb != nil && *emb && owned {
			return Preview{Kind: "desktop", URLs: []string{live}}
		}
		return c.screenshots(it)
	default:
		return frame(kind)
	}
}

func (c *ctx) screenshots(it *manifest.Item) Preview {
	p := Preview{Kind: "none", URLs: []string{}}
	for _, a := range it.Assets {
		if a.Role != manifest.AssetRoleScreenshot {
			continue
		}
		if img, ok := c.media.Image(string(a.Blob)); ok && len(img.Sizes) > 0 {
			p.Kind = "screenshot"
			p.URLs = append(p.URLs, c.url(img.Sizes[len(img.Sizes)-1].Path))
		}
	}
	return p
}

func previewCfg(pl manifest.Placement) (string, []string) {
	pv, _ := pl.Config["preview"].(map[string]any)
	kind, _ := pv["kind"].(string)
	var urls []string
	if raw, ok := pv["urls"].([]any); ok {
		for _, u := range raw {
			if s, ok := u.(string); ok {
				urls = append(urls, s)
			}
		}
	}
	return kind, urls
}

// thumbnail is the thumbnail asset, else a video's poster.
func (c *ctx) thumbnail(it *manifest.Item) *Asset {
	if a, had := c.image(it, manifest.AssetRoleThumbnail); a != nil {
		return a
	} else if had {
		c.warn("view-asset-pending", "%s's thumbnail isn't ready", it.Slug)
	}
	if it.Kind == manifest.KindVideo {
		return c.poster(it)
	}
	return nil
}

// Filters are the tech and tag slugs entries use, most used first.
type Filters struct {
	Tech []string `json:"tech"`
	Tags []string `json:"tags"`
}

func filters(es []Entry) Filters {
	count := func(get func(Entry) []string) []string {
		n := map[string]int{}
		seen := map[string]bool{}
		for _, e := range es {
			if seen[e.ID] { // an item placed twice counts once
				continue
			}
			seen[e.ID] = true
			for _, s := range get(e) {
				n[s]++
			}
		}
		out := make([]string, 0, len(n))
		for s := range n {
			out = append(out, s)
		}
		sort.Slice(out, func(i, j int) bool {
			if n[out[i]] != n[out[j]] {
				return n[out[i]] > n[out[j]]
			}
			return out[i] < out[j]
		})
		return out
	}
	return Filters{Tech: count(func(e Entry) []string { return e.Tech }), Tags: count(func(e Entry) []string { return e.Tags })}
}

// --- nickel-desktop -----------------------------------------------------

type desktopView struct {
	Header
	Entries []desktopEntry `json:"entries"`
}

type desktopEntry struct {
	Entry
	Open      desktopOpen `json:"open"`
	Pinned    *int        `json:"pinned"`
	Component *string     `json:"component"`
}

type desktopOpen struct {
	Type   string         `json:"type"`
	Route  *string        `json:"route"`
	URL    *string        `json:"url"`
	NewTab bool           `json:"newTab"`
	Window *desktopWindow `json:"window"`
}

type desktopWindow struct {
	Width  *int `json:"width"`
	Height *int `json:"height"`
}

func desktop(c *ctx) any {
	var es []desktopEntry
	var pinned []int
	for _, p := range c.placements() {
		var open desktopOpen
		if r := cfgString(p.pl, "route"); r != "" {
			open = desktopOpen{Type: "route", Route: &r}
		} else if u := liveLink(p.it); u != "" {
			open = desktopOpen{Type: "url", URL: &u, NewTab: cfgString(p.pl, "open") != "in-window"}
		} else {
			c.warn("view-no-target", "%s has no route and no live link, so the desktop can't open it", p.it.Slug)
			continue
		}
		e := desktopEntry{Entry: c.entry(p.it, &p.pl), Open: open}
		if w, ok := p.pl.Config["window"].(map[string]any); ok {
			e.Open.Window = &desktopWindow{Width: intOf(w["width"]), Height: intOf(w["height"])}
		}
		if comp := cfgString(p.pl, "component"); comp != "" {
			e.Component = &comp
		} else if rd := c.copyFor(p.it, &p.pl).RichDescription; rd != nil && rd.Component != nil {
			s := string(*rd.Component)
			e.Component = &s
		}
		if cfgBool(p.pl, "pinned") {
			pinned = append(pinned, len(es))
		}
		es = append(es, e)
	}
	isPinned := map[*desktopEntry]bool{}
	for _, i := range pinned {
		isPinned[&es[i]] = true
	}
	ptrs := make([]*desktopEntry, len(es))
	for i := range es {
		ptrs[i] = &es[i]
	}
	sortEntries(ptrs, func(e *desktopEntry) *Entry { return &e.Entry })
	out := make([]desktopEntry, 0, len(es))
	n := 0
	for _, e := range ptrs {
		if isPinned[e] {
			pos := n
			e.Pinned = &pos
			n++
		}
		out = append(out, *e)
	}
	return desktopView{Header: c.header(), Entries: out}
}

func intOf(v any) *int {
	if f, ok := v.(float64); ok {
		n := int(f)
		return &n
	}
	if n, ok := v.(int); ok {
		return &n
	}
	return nil
}

// --- software-portfolio and design-portfolio ----------------------------

type softwareView struct {
	Header
	Profile manifest.Profile `json:"profile"`
	Filters Filters          `json:"filters"`
	Entries []softwareEntry  `json:"entries"`
}

type softwareEntry struct {
	Entry
	Preview         Preview  `json:"preview"`
	Thumbnail       *Asset   `json:"thumbnail"`
	RichDescription *richOut `json:"richDescription"`
}

type richOut struct {
	MarkdownURL *string `json:"markdownUrl"`
	Component   *string `json:"component"`
}

func software(c *ctx) any {
	var es []softwareEntry
	for _, p := range c.placements() {
		e := softwareEntry{Entry: c.entry(p.it, &p.pl), Thumbnail: c.thumbnail(p.it)}
		kind, urls := previewCfg(p.pl)
		e.Preview = c.preview(p.it, kind, urls)
		if rd := c.copyFor(p.it, &p.pl).RichDescription; rd != nil {
			r := &richOut{}
			if rd.Component != nil {
				s := string(*rd.Component)
				r.Component = &s
			}
			// Markdown blobs can't be ingested yet (no markdown type), so
			// markdownUrl stays null until they can.
			e.RichDescription = r
		}
		es = append(es, e)
	}
	sortEntries(es, func(e softwareEntry) *Entry { return &e.Entry })
	var base []Entry
	for _, e := range es {
		base = append(base, e.Entry)
	}
	return softwareView{Header: c.header(), Profile: c.m.Profile, Filters: filters(base), Entries: orEmpty(es)}
}

type designView struct {
	Header
	Filters Filters       `json:"filters"`
	Entries []designEntry `json:"entries"`
}

type designEntry struct {
	Entry
	Preview   Preview `json:"preview"`
	Thumbnail *Asset  `json:"thumbnail"`
}

func design(c *ctx) any {
	var es []designEntry
	for _, p := range c.placements() {
		e := designEntry{Entry: c.entry(p.it, &p.pl), Thumbnail: c.thumbnail(p.it)}
		kind, urls := previewCfg(p.pl)
		e.Preview = c.preview(p.it, kind, urls)
		es = append(es, e)
	}
	sortEntries(es, func(e designEntry) *Entry { return &e.Entry })
	var base []Entry
	for _, e := range es {
		base = append(base, e.Entry)
	}
	return designView{Header: c.header(), Filters: filters(base), Entries: orEmpty(es)}
}

// --- art-portfolio --------------------------------------------------------

type artPortfolioView struct {
	Header
	Entries []artEntry `json:"entries"`
}

type artEntry struct {
	Entry
	Section     string `json:"section"`
	Image       *Asset `json:"image"`
	Cover       *Asset `json:"cover"`
	MemberCount *int   `json:"memberCount"`
}

var imageRoles = []manifest.AssetRole{manifest.AssetRoleOriginal, manifest.AssetRoleCover, manifest.AssetRoleThumbnail, manifest.AssetRoleScreenshot}

func artPortfolio(c *ctx) any {
	var es []artEntry
	for _, p := range c.placements() {
		e := artEntry{Entry: c.entry(p.it, &p.pl), Section: cfgString(p.pl, "section")}
		if p.it.Kind == manifest.KindCollection {
			if e.Section == "" {
				e.Section = "collections"
			}
			n := len(p.it.Members)
			e.MemberCount = &n
			e.Cover = c.cover(p.it)
		} else {
			if e.Section == "" {
				e.Section = "primary"
			}
			img, had := c.image(p.it, imageRoles...)
			if img == nil && had {
				c.warn("view-asset-pending", "%s left out: its image sizes aren't ready", p.it.Slug)
				continue
			}
			e.Image = img
		}
		es = append(es, e)
	}
	sortEntries(es, func(e artEntry) *Entry { return &e.Entry })
	return artPortfolioView{Header: c.header(), Entries: orEmpty(es)}
}

// cover is a collection's cover asset, else its first member with an image.
func (c *ctx) cover(it *manifest.Item) *Asset {
	if a, _ := c.image(it, manifest.AssetRoleCover, manifest.AssetRoleThumbnail); a != nil {
		return a
	}
	for _, id := range it.Members {
		if m := c.byID[id]; m != nil {
			if a, _ := c.image(m, imageRoles...); a != nil {
				return a
			}
		}
	}
	return nil
}

// --- holodeck-showroom ----------------------------------------------------

type showroomView struct {
	Header
	Profile manifest.Profile `json:"profile"`
	Entries []showroomEntry  `json:"entries"`
}

type showroomEntry struct {
	Entry
	Thumbnail *Asset          `json:"thumbnail"`
	Preview   showroomPreview `json:"preview"`
	Viewport  *viewport       `json:"viewport"`
	Light     *string         `json:"light"`
}

type showroomPreview struct {
	Kind     string   `json:"kind"`
	URLs     []string `json:"urls"`
	EmbedURL *string  `json:"embedUrl"`
}

type viewport struct {
	Slug   string `json:"slug"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

func showroom(c *ctx) any {
	var es []showroomEntry
	for _, p := range c.placements() {
		e := showroomEntry{Entry: c.entry(p.it, &p.pl), Thumbnail: c.thumbnail(p.it)}
		if e.Thumbnail == nil {
			if a, _ := c.image(p.it, manifest.AssetRoleScreenshot); a != nil {
				e.Thumbnail = a
			}
		}
		vp := cfgString(p.pl, "viewport")
		for _, v := range c.m.Taxonomy.Viewports {
			if string(v.Slug) == vp {
				e.Viewport = &viewport{Slug: vp, Width: v.Width, Height: v.Height}
			}
		}
		// The showroom shows the software portfolio's projects, so a
		// project's preview there follows its software-portfolio one (as
		// the 3D site's old sync did); otherwise auto, phones for phone
		// viewports.
		kind, urls := "auto", []string(nil)
		if strings.HasPrefix(vp, "phone") {
			kind = "phones"
		}
		for _, sp := range p.it.Placements {
			if sp.Surface == "software-portfolio" && (sp.Hidden == nil || !*sp.Hidden) {
				if k, u := previewCfg(sp); k != "" {
					kind, urls = k, u
				}
			}
		}
		pv := c.preview(p.it, kind, urls)
		if kind == "phones" && pv.Kind == "desktop" {
			pv.Kind = "phones"
		}
		e.Preview = showroomPreview{Kind: pv.Kind, URLs: pv.URLs}
		if pv.Kind == "desktop" || pv.Kind == "phones" || pv.Kind == "laser-room" {
			u := pv.URLs[0]
			e.Preview.EmbedURL = &u
		}
		if l := cfgString(p.pl, "light"); l != "" {
			e.Light = &l
		}
		es = append(es, e)
	}
	sortEntries(es, func(e showroomEntry) *Entry { return &e.Entry })
	return showroomView{Header: c.header(), Profile: c.m.Profile, Entries: orEmpty(es)}
}

// --- holodeck-art-room ----------------------------------------------------

type artRoomView struct {
	Header
	Entries []artRoomEntry `json:"entries"`
}

type artRoomEntry struct {
	Entry
	Pieces []piece `json:"pieces"`
}

type piece struct {
	ID          string  `json:"id"`
	Slug        string  `json:"slug"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Date        *Date   `json:"date"`
	Image       Asset   `json:"image"`
}

// artRoom: each placed collection is a gallery hall, its public members
// its pieces in order. A piece's own placement here can hide it or show its
// title (titled).
func artRoom(c *ctx) any {
	var es []artRoomEntry
	for _, p := range c.placements() {
		if p.it.Kind != manifest.KindCollection {
			continue
		}
		e := artRoomEntry{Entry: c.entry(p.it, &p.pl), Pieces: []piece{}}
		for _, id := range p.it.Members {
			m := c.byID[id]
			if m == nil || (m.Kind != manifest.KindArtwork && m.Kind != manifest.KindPhoto) {
				continue
			}
			pl, has, hidden := c.placementOf(m)
			if hidden {
				continue
			}
			img, had := c.image(m, imageRoles...)
			if img == nil {
				if had {
					c.warn("view-asset-pending", "%s left out of %s: its image sizes aren't ready", m.Slug, p.it.Slug)
				}
				continue
			}
			var plp *manifest.Placement
			if has {
				plp = &pl
			}
			cp := c.copyFor(m, plp)
			pc := piece{ID: string(m.ID), Slug: string(m.Slug), Image: *img}
			if has && cfgBool(pl, "titled") {
				pc.Title = cp.Title
			}
			if s := deref(cp.Summary); s != "" {
				pc.Description = &s
			}
			if m.Date != nil {
				d := ResolveDate(string(m.Date.Value), m.Date.Label)
				pc.Date = &d
			}
			e.Pieces = append(e.Pieces, pc)
		}
		es = append(es, e)
	}
	sortEntries(es, func(e artRoomEntry) *Entry { return &e.Entry })
	return artRoomView{Header: c.header(), Entries: orEmpty(es)}
}

// --- holodeck-dj-room -----------------------------------------------------

type djView struct {
	Header
	Artist  string    `json:"artist"`
	Tracks  []djTrack `json:"tracks"`
	Entries []Entry   `json:"entries"`
}

type djTrack struct {
	ID        string  `json:"id"`
	Slug      string  `json:"slug"`
	Title     string  `json:"title"`
	Artist    string  `json:"artist"`
	Audio     string  `json:"audio"`
	Analysis  string  `json:"analysis"`
	Duration  float64 `json:"duration"`
	BPM       float64 `json:"bpm"`
	FirstBeat float64 `json:"firstBeat"`
	Start     float64 `json:"start"`
	Key       string  `json:"key"`
	Camelot   string  `json:"camelot"`
}

// djRoom: the placed collections' tracks in member order, then tracks
// placed on their own, by placement order (D15). The crate's artist is the
// collection's credit, else the tracks' most common artist, else the
// profile name.
func djRoom(c *ctx) any {
	var entries []Entry
	var order []*manifest.Item
	seen := map[manifest.Ulid]bool{}
	var collCredit string
	var direct []placed
	for _, p := range c.placements() {
		if p.it.Kind == manifest.KindCollection {
			entries = append(entries, c.entry(p.it, &p.pl))
			if collCredit == "" && len(p.it.Credits) > 0 {
				collCredit = p.it.Credits[0].Name
			}
			continue
		}
		if p.it.Kind == manifest.KindTrack {
			direct = append(direct, p)
		}
	}
	sortEntries(entries, func(e Entry) *Entry { return &e })
	for _, e := range entries {
		for _, id := range c.byID[manifest.Ulid(e.ID)].Members {
			if m := c.byID[id]; m != nil && m.Kind == manifest.KindTrack && !seen[id] {
				if _, _, hidden := c.placementOf(m); !hidden {
					seen[id] = true
					order = append(order, m)
				}
			}
		}
	}
	sort.SliceStable(direct, func(i, j int) bool {
		a, b := direct[i].pl.Order, direct[j].pl.Order
		if (a == nil) != (b == nil) {
			return a != nil
		}
		return a != nil && *a < *b
	})
	for _, p := range direct {
		if !seen[p.it.ID] {
			seen[p.it.ID] = true
			order = append(order, p.it)
		}
	}

	artists := map[string]int{}
	tracks := []djTrack{}
	for _, it := range order {
		t, ok := c.track(it)
		if !ok {
			continue
		}
		artists[t.Artist]++
		tracks = append(tracks, t)
	}
	artist := collCredit
	if artist == "" {
		best := 0
		for a, n := range artists {
			if a != "" && (n > best || (n == best && a < artist)) {
				artist, best = a, n
			}
		}
	}
	if artist == "" {
		artist = c.m.Profile.Name
	}
	for i := range tracks {
		if tracks[i].Artist == "" {
			tracks[i].Artist = artist
		}
	}
	return djView{Header: c.header(), Artist: artist, Tracks: tracks, Entries: orEmpty(entries)}
}

func (c *ctx) track(it *manifest.Item) (djTrack, bool) {
	f := it.Facts
	num := func(k string) (float64, bool) { v, ok := f[k].(float64); return v, ok }
	str := func(k string) (string, bool) { v, ok := f[k].(string); return v, ok && v != "" }
	dur, ok1 := num("duration")
	bpm, ok2 := num("bpm")
	beat, ok3 := num("firstBeat")
	start, ok4 := num("start")
	key, ok5 := str("key")
	cam, ok6 := str("camelot")
	if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6) {
		c.warn("view-track-facts", "%s left out: it needs duration, bpm, firstBeat, start, key and camelot", it.Slug)
		return djTrack{}, false
	}
	var tr Track
	found := false
	for _, a := range it.Assets {
		if a.Role == manifest.AssetRoleAudio {
			tr, found = c.media.Track(string(a.Blob))
			if found {
				break
			}
		}
	}
	if !found {
		c.warn("view-asset-pending", "%s left out: its audio or analysis isn't ready", it.Slug)
		return djTrack{}, false
	}
	t := djTrack{ID: string(it.ID), Slug: string(it.Slug), Title: it.Title, Audio: c.url(tr.Audio), Analysis: c.url(tr.Analysis),
		Duration: dur, BPM: bpm, FirstBeat: beat, Start: start, Key: key, Camelot: cam}
	for _, cr := range it.Credits {
		if cr.Role == nil || *cr.Role == "artist" {
			t.Artist = cr.Name
			break
		}
	}
	return t, true
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
