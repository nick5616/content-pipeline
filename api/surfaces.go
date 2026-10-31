package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"

	"github.com/nick5616/canon/engine/gcs"
	"github.com/nick5616/canon/engine/items"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/plan"
	"github.com/nick5616/canon/engine/publish"
	"github.com/nick5616/canon/engine/surfaces"
	"github.com/nick5616/canon/engine/views"
)

// SurfaceInfo is a declared surface (openapi SurfaceInfo).
type SurfaceInfo struct {
	ID          string  `json:"id"`
	Label       string  `json:"label"`
	BaseURL     *string `json:"baseUrl"`
	Registered  bool    `json:"registered"`
	ViewVersion *int    `json:"viewVersion"`
	Placements  int     `json:"placements"`
}

// Surfaces lists the surfaces the working manifest declares.
func (e *Engine) Surfaces() ([]SurfaceInfo, error) {
	m, _, _, err := e.Working.Load()
	if err != nil {
		return nil, err
	}
	reg := surfaces.MustDefault()
	out := []SurfaceInfo{}
	for _, s := range m.Surfaces {
		si := SurfaceInfo{ID: string(s.ID), Label: s.Label}
		if s.BaseURL != nil {
			u := string(*s.BaseURL)
			si.BaseURL = &u
		}
		if _, ok := reg.Get(string(s.ID)); ok {
			v := 1
			si.Registered, si.ViewVersion = true, &v
		}
		for _, it := range m.Items {
			for _, pl := range it.Placements {
				if pl.Surface == s.ID && (pl.Hidden == nil || !*pl.Hidden) {
					si.Placements++
				}
			}
		}
		out = append(out, si)
	}
	return out, nil
}

// Preview resolves a surface's view from the working manifest without
// publishing (canon view <surface>). drafts previews placed drafts too. It
// works without buckets: job outputs then come from the local cache only.
func (e *Engine) Preview(ctx context.Context, surface string, drafts bool) ([]byte, []manifest.Problem, error) {
	m, _, _, err := e.Working.Load()
	if err != nil {
		return nil, nil, err
	}
	env := plan.Env{Working: e.Working, Blobs: e.Blobs, Derived: e.Derived, ArchiveBucket: e.Config.ArchiveBucket,
		PublicBase: e.Config.PublicBase()}
	if env.PublicBase == "" {
		env.PublicBase = "https://storage.googleapis.com/canon-public-not-configured"
	}
	if r, err := e.remote(ctx); err == nil {
		env.Remote = r
	}
	return plan.PreviewView(ctx, m, env, surface, drafts)
}

func (a *engineAPI) listSurfaces(w http.ResponseWriter, r *http.Request) {
	ss, err := a.e.Surfaces()
	if err != nil {
		engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ss)
}

func (a *engineAPI) previewSurface(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	known := false
	for _, s := range views.Surfaces() {
		known = known || s == id
	}
	if !known {
		problem(w, http.StatusNotFound, "No such surface", id+" has no view")
		return
	}
	b, _, err := a.e.Preview(r.Context(), id, false)
	if err != nil {
		engineError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(json.RawMessage(b))
}

func (a *engineAPI) putSurface(w http.ResponseWriter, r *http.Request) {
	rev, ok := ifMatch(w, r)
	if !ok {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	if _, newRev, err := items.SetSurface(a.e.Working, rev, r.PathValue("id"), body); err != nil {
		engineError(w, err)
	} else {
		w.Header().Set("ETag", etag(newRev))
		ss, err := a.e.Surfaces()
		if err != nil {
			engineError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ss)
	}
}

// EntryRef names one view entry in SurfaceChanges.
type EntryRef struct {
	ID     string   `json:"id"`
	Key    *string  `json:"key"`
	Slug   string   `json:"slug"`
	Title  string   `json:"title"`
	Fields []string `json:"fields,omitempty"`
}

// SurfaceChanges is what the next apply changes in a view (openapi).
type SurfaceChanges struct {
	Surface string     `json:"surface"`
	Live    bool       `json:"live"`
	Added   []EntryRef `json:"added"`
	Removed []EntryRef `json:"removed"`
	Changed []EntryRef `json:"changed"`
	Header  bool       `json:"header"`
}

// Changes compares surface's preview with its live view in the public
// bucket, entry by entry (matched by id and placement key).
func (e *Engine) Changes(ctx context.Context, surface string) (SurfaceChanges, error) {
	out := SurfaceChanges{Surface: surface, Added: []EntryRef{}, Removed: []EntryRef{}, Changed: []EntryRef{}}
	next, _, err := e.Preview(ctx, surface, false)
	if err != nil {
		return out, err
	}
	var live []byte
	if e.Config.PublicBucket != "" {
		api, err := e.GCS(ctx)
		if err != nil {
			return out, err
		}
		b, _, err := api.Read(ctx, e.Config.PublicBucket, publish.ViewPath(surface))
		switch {
		case errors.Is(err, gcs.ErrNotExist):
		case err != nil:
			return out, err
		default:
			live, out.Live = b, true
		}
	}
	type entry = map[string]json.RawMessage
	parse := func(b []byte) (map[string]json.RawMessage, []entry) {
		var v map[string]json.RawMessage
		var es []entry
		if len(b) > 0 && json.Unmarshal(b, &v) == nil {
			json.Unmarshal(v["entries"], &es)
			delete(v, "entries")
		}
		return v, es
	}
	ref := func(en entry) (string, EntryRef) {
		var r EntryRef
		json.Unmarshal(en["id"], &r.ID)
		json.Unmarshal(en["key"], &r.Key)
		json.Unmarshal(en["slug"], &r.Slug)
		json.Unmarshal(en["title"], &r.Title)
		k := r.ID
		if r.Key != nil {
			k += "#" + *r.Key
		}
		return k, r
	}
	nh, ne := parse(next)
	lh, le := parse(live)
	old := map[string]entry{}
	for _, en := range le {
		k, _ := ref(en)
		old[k] = en
	}
	seen := map[string]bool{}
	for _, en := range ne {
		k, r := ref(en)
		seen[k] = true
		o, ok := old[k]
		if !ok {
			out.Added = append(out.Added, r)
			continue
		}
		for f, v := range en {
			if !bytes.Equal(v, o[f]) {
				r.Fields = append(r.Fields, f)
			}
		}
		for f := range o {
			if _, ok := en[f]; !ok {
				r.Fields = append(r.Fields, f)
			}
		}
		if len(r.Fields) > 0 {
			sort.Strings(r.Fields)
			out.Changed = append(out.Changed, r)
		}
	}
	for _, en := range le {
		if k, r := ref(en); !seen[k] {
			out.Removed = append(out.Removed, r)
		}
	}
	if out.Live {
		nb, _ := json.Marshal(nh)
		lb, _ := json.Marshal(lh)
		out.Header = !bytes.Equal(nb, lb)
	}
	return out, nil
}

func (a *engineAPI) surfaceChanges(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !slices.Contains(views.Surfaces(), id) {
		problem(w, http.StatusNotFound, "No such surface", id+" has no view")
		return
	}
	ch, err := a.e.Changes(r.Context(), id)
	if err != nil {
		engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ch)
}
