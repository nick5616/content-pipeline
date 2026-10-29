// Package importbucket drafts items from an existing art and media bucket
// (migration step 2, done early; D43):
//
//	list -> download -> ingest (sanitize, verify, store) -> metadata -> collections
//
// The source bucket is only listed and read. Each file goes through the
// same pipeline as a dropped file, so its original is kept for the archive
// and the next plan uploads only sanitized bytes. Rerunning is safe: files
// already in the manifest keep their edits and only gain missing
// collection memberships.
package importbucket

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/nick5616/canon/engine/gcs"
	"github.com/nick5616/canon/engine/ids"
	"github.com/nick5616/canon/engine/ingest"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/store"
)

// Status is one object's outcome.
type Status string

const (
	Imported Status = "imported" // a new draft
	Existing Status = "existing" // already in the manifest; only memberships added
	Skipped  Status = "skipped"  // not content (folder marker, track analysis)
	Failed   Status = "failed"
	Checked  Status = "checked" // dry run: sanitized and verified, nothing stored
)

// Result is one object's outcome.
type Result struct {
	Object     string   `json:"object"`
	Status     Status   `json:"status"`
	Item       string   `json:"item,omitempty"`
	Slug       string   `json:"slug,omitempty"`
	Collection string   `json:"collection,omitempty"`
	Note       string   `json:"note,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// Report is a whole import. It doubles as the old-object-to-item map that
// M5 turns into old-URL-to-new-URL redirects.
type Report struct {
	Bucket  string   `json:"bucket"`
	Prefix  string   `json:"prefix,omitempty"`
	Results []Result `json:"results"`
}

// Failed counts failed objects.
func (r Report) Failed() int {
	n := 0
	for _, x := range r.Results {
		if x.Status == Failed {
			n++
		}
	}
	return n
}

// Options change one import.
type Options struct {
	Prefix string // only objects under this prefix
	DryRun bool   // sanitize and verify only; store nothing
}

// Importer holds what an import needs.
type Importer struct {
	GCS     gcs.API
	Ingest  *ingest.Ingester
	Working *store.Working
	IDs     *ids.Generator // nil uses ids.New
}

// folder is a top-level folder's collection. Folder names are the
// holodeck's collection ids, so the slug keeps them, except dj-crate (D15).
type folder struct{ slug, title string }

var folders = map[string]folder{
	"digitalart":    {"digitalart", "Digital Art"},
	"dj-crate":      {"music", "Music"},
	"lefthanded":    {"lefthanded", "Left-Handed Art"},
	"miscellaneous": {"miscellaneous", "Miscellaneous"},
	"notesappart":   {"notesappart", "Notes App Art"},
	"paintings":     {"paintings", "Paintings"},
	"sketches":      {"sketches", "Sketches"},
}

const crateIndex = "dj-crate/crate.json"

// crateTrack is one track in dj-crate/crate.json, from build-dj-crate.mjs.
type crateTrack struct {
	Title     string   `json:"title"`
	Artist    string   `json:"artist"`
	Audio     string   `json:"audio"`
	BPM       *float64 `json:"bpm"`
	FirstBeat *float64 `json:"firstBeat"`
	Start     *float64 `json:"start"`
	Key       string   `json:"key"`
	Camelot   string   `json:"camelot"`
}

// member is an item headed for a folder's collection.
type member struct {
	id    manifest.Ulid
	order *int
	name  string
}

// Import drafts every content object in bucket.
func (im *Importer) Import(ctx context.Context, bucket string, opts Options) (Report, error) {
	rep := Report{Bucket: bucket, Prefix: opts.Prefix}
	objs, err := im.GCS.List(ctx, bucket, opts.Prefix)
	if err != nil {
		return rep, fmt.Errorf("list gs://%s: %w", bucket, err)
	}
	crate, crateErr := im.crate(ctx, bucket, objs)

	members := map[string][]member{} // folder -> members, in listing order
	for _, o := range objs {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		r := Result{Object: o.Name}
		if skip := skipReason(o); skip != "" {
			r.Status, r.Note = Skipped, skip
			rep.Results = append(rep.Results, r)
			continue
		}
		var track *crateTrack
		if dir, base := split(o.Name); dir == "dj-crate" {
			if t, ok := crate[base]; ok {
				track = &t
			} else if crateErr != nil {
				r.Warnings = append(r.Warnings, crateErr.Error())
			} else {
				r.Warnings = append(r.Warnings, "not listed in "+crateIndex)
			}
		}
		im.importOne(ctx, bucket, o, track, opts, &r)
		if r.Item != "" && !opts.DryRun {
			if dir, _ := split(o.Name); dir != "" {
				members[dir] = append(members[dir], member{manifest.Ulid(r.Item), artOrder(o.Metadata, &r), o.Name})
			}
		}
		rep.Results = append(rep.Results, r)
	}
	if opts.DryRun {
		return rep, nil
	}
	return rep, im.collect(members, &rep)
}

// skipReason says why an object isn't content, or "" if it is.
func skipReason(o gcs.Object) string {
	switch dir, base := split(o.Name); {
	case strings.HasSuffix(o.Name, "/"):
		return "folder marker"
	case o.Name == crateIndex:
		return "track index; its titles and beat grids go on the tracks"
	case dir == "dj-crate" && strings.HasSuffix(base, ".json"):
		return "track analysis; djgrid@v1 rebuilds it (M4)"
	}
	return ""
}

// crate reads dj-crate/crate.json, keyed by audio file name. It is only
// read when the listing has it.
func (im *Importer) crate(ctx context.Context, bucket string, objs []gcs.Object) (map[string]crateTrack, error) {
	found := false
	for _, o := range objs {
		found = found || o.Name == crateIndex
	}
	if !found {
		return nil, nil
	}
	b, _, err := im.GCS.Read(ctx, bucket, crateIndex)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", crateIndex, err)
	}
	var c struct {
		Tracks []crateTrack `json:"tracks"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", crateIndex, err)
	}
	out := map[string]crateTrack{}
	for _, t := range c.Tracks {
		out[t.Audio] = t
	}
	return out, nil
}

// importOne downloads, ingests and annotates one object.
func (im *Importer) importOne(ctx context.Context, bucket string, o gcs.Object, track *crateTrack, opts Options, r *Result) {
	fail := func(err error) { r.Status, r.Error = Failed, err.Error() }
	b, _, err := im.GCS.Read(ctx, bucket, o.Name)
	if err != nil {
		fail(err)
		return
	}
	staged, err := im.Ingest.StageReader(bytes.NewReader(b), path.Base(o.Name))
	if err != nil {
		fail(err)
		return
	}
	// Ingest picks track for audio. An image with a camera in its EXIF
	// becomes a photo draft; annotate makes it an artwork, since the plan
	// imports one artwork per image (the bucket's sketches are iPhone shots).
	fr := im.Ingest.Process(ctx, staged, ingest.Options{DryRun: opts.DryRun}, nil)
	if fr.Stage == ingest.Failed {
		fail(fmt.Errorf("%s", deref(fr.Error)))
		return
	}
	if opts.DryRun {
		r.Status = Checked
		return
	}
	r.Item, r.Slug = deref(fr.Item), fr.Slug
	if fr.Note != nil {
		r.Status, r.Note = Existing, *fr.Note
		// The same file can sit in two folders with different metadata. A
		// private marker on any copy must survive, so it is the one thing
		// added to an existing item.
		if isPrivate(o.Metadata) {
			r.Warnings = append(r.Warnings, "marked private in the old bucket")
			if err := im.notePrivate(manifest.Ulid(r.Item)); err != nil {
				r.Warnings = append(r.Warnings, "couldn't note it as private: "+err.Error())
			}
		}
		return
	}
	r.Status = Imported
	if err := im.annotate(manifest.Ulid(r.Item), bucket, o, track, r); err != nil {
		r.Warnings = append(r.Warnings, "kept as a plain draft; metadata not applied: "+err.Error())
	}
}

// annotate copies the old bucket's metadata onto a new draft. It touches
// only items this import just made.
func (im *Importer) annotate(id manifest.Ulid, bucket string, o gcs.Object, track *crateTrack, r *Result) error {
	md := o.Metadata
	var warn []string
	_, err := im.Working.Update(func(m *manifest.Manifest) error {
		it := find(m, id)
		if it == nil {
			return fmt.Errorf("item %s vanished", id)
		}
		if it.Kind != manifest.KindTrack {
			it.Kind = manifest.KindArtwork
			it.Facts = artworkFacts(it.Facts)
		}
		notes := []string{"Imported from gs://" + bucket + "/" + o.Name + "."}

		if t := strings.TrimSpace(md["art-name"]); t != "" {
			it.Title = clip(t, 200)
		}
		if d := strings.TrimSpace(md["art-description"]); d != "" {
			s := clip(d, 2000)
			it.Summary = &s
		}
		date, w := pickDate(md)
		warn = append(warn, w...)
		if date != nil {
			it.Date = date
		}
		for _, w := range w {
			notes = append(notes, w+".")
		}
		if isPrivate(md) {
			notes = append(notes, privateNote)
			warn = append(warn, "marked private in the old bucket")
		}
		if track != nil {
			applyTrack(it, *track)
		}
		n := strings.Join(notes, " ")
		if it.Private != nil && it.Private.Notes != nil {
			n = *it.Private.Notes + "\n" + n
		}
		it.Private = &manifest.Private{Notes: &n}
		return nil
	})
	r.Warnings = append(r.Warnings, warn...)
	return err
}

const privateNote = "Marked private in the old bucket: keep it unpublished."

func isPrivate(md map[string]string) bool {
	return strings.EqualFold(strings.TrimSpace(md["private"]), "true")
}

// notePrivate adds the private note to an existing item, once.
func (im *Importer) notePrivate(id manifest.Ulid) error {
	m, _, _, err := im.Working.Load()
	if err != nil {
		return err
	}
	if it := find(m, id); it != nil && it.Private != nil && strings.Contains(deref(it.Private.Notes), privateNote) {
		return nil
	}
	_, err = im.Working.Update(func(m *manifest.Manifest) error {
		it := find(m, id)
		if it == nil {
			return fmt.Errorf("item %s vanished", id)
		}
		n := privateNote
		if it.Private != nil && it.Private.Notes != nil {
			if strings.Contains(*it.Private.Notes, privateNote) {
				return nil
			}
			n = *it.Private.Notes + "\n" + n
		}
		it.Private = &manifest.Private{Notes: &n}
		return nil
	})
	return err
}

// artworkFacts keeps only the facts an artwork may have: a draft made as a
// photo carries the same pixel facts.
func artworkFacts(f manifest.ItemFacts) manifest.ItemFacts {
	out := manifest.ItemFacts{}
	for _, k := range []string{"pixelWidth", "pixelHeight", "palette"} {
		if v, ok := f[k]; ok {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// pickDate reads art-date (the holodeck's) and created (Nickel Tools'). When
// both parse and disagree, art-date wins and the conflict is flagged.
func pickDate(md map[string]string) (*manifest.Date, []string) {
	var warn []string
	var picked *manifest.Date
	for _, key := range []string{"art-date", "created"} {
		raw := strings.TrimSpace(md[key])
		if raw == "" {
			continue
		}
		v, label, ok := parseDate(raw)
		if !ok {
			warn = append(warn, fmt.Sprintf("%s %q isn't a date; left out", key, raw))
			continue
		}
		d := &manifest.Date{Value: manifest.IsoPartialDate(v)}
		if label != "" {
			d.Label = &label
		}
		if picked == nil {
			picked = d
		} else if picked.Value != d.Value {
			warn = append(warn, fmt.Sprintf("created %q disagrees with art-date; art-date kept", raw))
		}
	}
	return picked, warn
}

// applyTrack copies crate.json's title, artist and beat grid. djgrid@v1
// recomputes the grid in M4; until then these keep the DJ room's values.
func applyTrack(it *manifest.Item, t crateTrack) {
	if s := strings.TrimSpace(t.Title); s != "" {
		it.Title = clip(s, 200)
	}
	if a := strings.TrimSpace(t.Artist); a != "" {
		role := "artist"
		it.Credits = []manifest.Credit{{Name: a, Role: &role}}
	}
	if it.Facts == nil {
		it.Facts = manifest.ItemFacts{}
	}
	set := func(k string, v *float64) {
		if v != nil {
			it.Facts[k] = *v
		}
	}
	set("bpm", t.BPM)
	set("firstBeat", t.FirstBeat)
	set("start", t.Start)
	if t.Key != "" {
		it.Facts["key"] = t.Key
	}
	if t.Camelot != "" {
		it.Facts["camelot"] = t.Camelot
	}
}

// artOrder reads art-order (lower first). A bad value is flagged and sorts
// last.
func artOrder(md map[string]string, r *Result) *int {
	raw := strings.TrimSpace(md["art-order"])
	if raw == "" {
		return nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		r.Warnings = append(r.Warnings, fmt.Sprintf("art-order %q isn't a number; sorted last", raw))
		return nil
	}
	return &n
}

// collect puts each folder's items in its collection, creating the
// collection as a draft if needed. New members are ordered by art-order,
// then name, and go after the existing ones.
func (im *Importer) collect(members map[string][]member, rep *Report) error {
	if len(members) == 0 {
		return nil
	}
	dirs := make([]string, 0, len(members))
	for d := range members {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	slugOf := map[string]string{}
	_, err := im.Working.Update(func(m *manifest.Manifest) error {
		for _, dir := range dirs {
			f, ok := folders[dir]
			if !ok {
				f = folder{dir, dir}
			}
			slugOf[dir] = f.slug
			c := collection(m, f.slug)
			if c == nil {
				m.Items = append(m.Items, manifest.Item{
					ID: im.newID(), Slug: manifest.Slug(f.slug), Kind: manifest.KindCollection,
					Visibility: manifest.ItemVisibilityDraft, Title: f.title,
				})
				c = &m.Items[len(m.Items)-1]
			}
			ms := members[dir]
			sort.SliceStable(ms, func(i, j int) bool {
				a, b := ms[i], ms[j]
				if (a.order == nil) != (b.order == nil) {
					return a.order != nil
				}
				if a.order != nil && *a.order != *b.order {
					return *a.order < *b.order
				}
				return a.name < b.name
			})
			have := map[manifest.Ulid]bool{}
			for _, id := range c.Members {
				have[id] = true
			}
			for _, x := range ms {
				if !have[x.id] {
					c.Members = append(c.Members, x.id)
					have[x.id] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("collections: %w", err)
	}
	for i := range rep.Results {
		r := &rep.Results[i]
		if dir, _ := split(r.Object); r.Item != "" && slugOf[dir] != "" {
			r.Collection = slugOf[dir]
		}
	}
	return nil
}

// collection finds the collection with slug, or nil. A non-collection item
// with that slug makes the validator refuse the update, which is the right
// outcome: the folder's collection needs a name chosen by hand.
func collection(m *manifest.Manifest, slug string) *manifest.Item {
	for i := range m.Items {
		if string(m.Items[i].Slug) == slug && m.Items[i].Kind == manifest.KindCollection {
			return &m.Items[i]
		}
	}
	return nil
}

func find(m *manifest.Manifest, id manifest.Ulid) *manifest.Item {
	for i := range m.Items {
		if m.Items[i].ID == id {
			return &m.Items[i]
		}
	}
	return nil
}

func (im *Importer) newID() manifest.Ulid {
	if im.IDs != nil {
		return manifest.Ulid(im.IDs.New().String())
	}
	return manifest.Ulid(ids.New().String())
}

// split returns an object's top-level folder ("" at the root) and base name.
func split(name string) (dir, base string) {
	if i := strings.Index(name, "/"); i >= 0 {
		return name[:i], path.Base(name)
	}
	return "", name
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
