package api

import (
	"net/http"
	"regexp"
	"strconv"

	"github.com/nick5616/canon/engine/derived"
	"github.com/nick5616/canon/engine/recipes"

	"github.com/nick5616/canon/engine/doctor"
	"github.com/nick5616/canon/engine/jobs"
	"github.com/nick5616/canon/engine/toolchain"
)

var (
	shaRe  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	fileRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
)

// localBlob serves a sanitized blob from the cache, for offline
// thumbnails. Only blobs (never originals) are reachable here.
func (a *engineAPI) localBlob(w http.ResponseWriter, r *http.Request) {
	sum := r.PathValue("sha256")
	if !shaRe.MatchString(sum) {
		problem(w, http.StatusNotFound, "No such blob", sum)
		return
	}
	b, ok := a.e.Blobs.Blob(sum)
	if !ok {
		problem(w, http.StatusNotFound, "No such blob", "sha256:"+sum+" isn't in the local cache")
		return
	}
	typ := "application/octet-stream"
	if t, ok := jobs.ByExt(b.Ext()); ok {
		typ = t.MIME
	}
	serveCached(w, r, b.Path(), typ)
}

// localDerived serves one file a finished job lists in its receipt.
func (a *engineAPI) localDerived(w http.ResponseWriter, r *http.Request) {
	key, name := r.PathValue("jobkey"), r.PathValue("file")
	if a.e.Derived == nil || !shaRe.MatchString(key) || !fileRe.MatchString(name) {
		problem(w, http.StatusNotFound, "No such file", key+"/"+name)
		return
	}
	d, ok := a.e.Derived.Lookup(key)
	if !ok {
		problem(w, http.StatusNotFound, "No such job", key+" isn't in the local cache")
		return
	}
	for _, f := range d.Receipt().Files {
		if f.Name == name {
			serveCached(w, r, d.Path(name), f.Type)
			return
		}
	}
	problem(w, http.StatusNotFound, "No such file", name+" isn't one of job "+key+"'s outputs")
}

// serveCached serves a content-addressed file: it never changes, so the
// browser may keep it.
func serveCached(w http.ResponseWriter, r *http.Request, path, typ string) {
	w.Header().Set("Content-Type", typ)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, path)
}

func (a *engineAPI) getDoctor(w http.ResponseWriter, r *http.Request) {
	if a.e.Doctor == nil {
		problem(w, http.StatusServiceUnavailable, "Doctor unavailable", "canon serve didn't wire the doctor.")
		return
	}
	rep := a.e.Doctor(r.Context())
	if rep.Checks == nil {
		rep.Checks = []doctor.Check{}
	}
	if rep.Tools == nil {
		rep.Tools = []toolchain.Tool{}
	}
	writeJSON(w, http.StatusOK, rep)
}

// localThumbnail serves an image blob's thumb@vN output nearest w: the
// smallest size at least w wide, else the largest.
func (a *engineAPI) localThumbnail(w http.ResponseWriter, r *http.Request) {
	sum := r.PathValue("sha256")
	want := 320
	if q := r.URL.Query().Get("w"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 1 || n > 4000 {
			problem(w, http.StatusBadRequest, "Bad width", q)
			return
		}
		want = n
	}
	thumb, _ := recipes.Find("thumb")
	if a.e.Derived == nil || !shaRe.MatchString(sum) {
		problem(w, http.StatusNotFound, "No thumbnail", sum)
		return
	}
	d, ok := a.e.Derived.Lookup(jobs.Key(sum, thumb))
	if !ok {
		problem(w, http.StatusNotFound, "No thumbnail", "the thumb job for sha256:"+sum+" hasn't run here")
		return
	}
	var best *derived.File
	for _, f := range d.Receipt().Files {
		f := f
		switch {
		case best == nil:
			best = &f
		case best.Width < want && f.Width > best.Width:
			best = &f
		case f.Width >= want && f.Width < best.Width:
			best = &f
		}
	}
	if best == nil {
		problem(w, http.StatusNotFound, "No thumbnail", "the thumb job for sha256:"+sum+" made no files")
		return
	}
	w.Header().Set("Content-Type", best.Type)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, d.Path(best.Name))
}
