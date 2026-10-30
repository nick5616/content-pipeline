package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/nick5616/canon/engine/items"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/plan"
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
