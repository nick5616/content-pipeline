package plan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/nick5616/canon/engine/derived"
	"github.com/nick5616/canon/engine/gcs"
	"github.com/nick5616/canon/engine/jobs"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/media"
	"github.com/nick5616/canon/engine/publish"
	"github.com/nick5616/canon/engine/surfaces"
	"github.com/nick5616/canon/engine/views"
)

// PublicState is what the public bucket should hold (M5).
type PublicState struct {
	Manifest []byte            // the public projection
	Views    map[string][]byte // by surface
	// Files are the objects views link, by name, with the checksum the
	// private bucket's copy has (D51).
	Files map[string]PublicFile
	// Images maps public item ids to their main image URL (redirects).
	Images map[string]string
	Items  int // public items
}

// PublicFile is one file to publish.
type PublicFile struct {
	SHA256 string
	Size   int64
	Item   string // slug, for the plan line
}

// Sidecar is the bytes a plan writes to the public bucket, kept beside the
// plan (<id>.public.json) so apply writes exactly what was reviewed. It is
// all public data.
type Sidecar struct {
	Manifest string            `json:"manifest"`
	Views    map[string]string `json:"views"`
	Images   map[string]string `json:"images"`
}

// ErrNoPublicBucket means items are public but config has no public bucket.
var ErrNoPublicBucket = errors.New("items are public, but config has no publicBucket to publish them to")

// receipts reads job receipts and files locally, else from the private
// bucket, caching what it downloads.
type receipts struct {
	der     *derived.Store
	api     gcs.API
	bucket  string
	derived map[string]map[string]gcs.Object
	ctx     context.Context

	mu    sync.Mutex
	cache map[string]derived.Receipt
}

func (r *receipts) Receipt(key string) (derived.Receipt, bool) {
	if d, ok := r.der.Lookup(key); ok {
		return d.Receipt(), true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if rc, ok := r.cache[key]; ok {
		return rc, true
	}
	if _, ok := r.derived[key][derived.ReceiptName]; !ok {
		return derived.Receipt{}, false
	}
	b, _, err := r.api.Read(r.ctx, r.bucket, jobs.ReceiptPath(key))
	if err != nil {
		return derived.Receipt{}, false
	}
	var rc derived.Receipt
	if json.Unmarshal(b, &rc) != nil || rc.Jobkey != key {
		return derived.Receipt{}, false
	}
	r.cache[key] = rc
	return rc, true
}

func (r *receipts) File(key, name string) ([]byte, error) {
	if d, ok := r.der.Lookup(key); ok {
		return os.ReadFile(d.Path(name))
	}
	b, _, err := r.api.Read(r.ctx, r.bucket, jobs.FilePath(key, name))
	return b, err
}

// GatherPublic projects m, resolves every view from the projection, and
// lists the files they link. Problems are warnings (entries left out).
func GatherPublic(ctx context.Context, m *manifest.Manifest, env Env, l Listing) (*PublicState, []manifest.Problem, error) {
	pm := publish.Project(m)
	mb, err := publish.Marshal(pm)
	if err != nil {
		return nil, nil, err
	}
	pub := &PublicState{Manifest: mb, Views: map[string][]byte{}, Files: map[string]PublicFile{}, Images: map[string]string{}, Items: len(pm.Items)}
	rs := &receipts{der: env.Derived, api: env.Remote.API, bucket: env.Remote.Bucket, derived: l.Derived, ctx: ctx, cache: map[string]derived.Receipt{}}
	typeOf := blobType(env, l)
	jm := publish.NewJobMedia(pm, typeOf, rs)
	reg := surfaces.MustDefault()
	slugOf := map[string]string{}
	var ps []manifest.Problem
	for _, s := range views.Surfaces() {
		r, err := views.Resolve(pm, s, jm, env.PublicBase)
		if err != nil {
			return nil, nil, err
		}
		b, err := r.Bytes(reg)
		if err != nil {
			return nil, nil, err
		}
		pub.Views[s] = b
		ps = append(ps, r.Problems...)
		for id, u := range r.Images {
			pub.Images[id] = u
		}
		for f := range r.Files {
			slugOf[f] = s
		}
	}
	for name, surface := range slugOf {
		pf, err := publicFile(name, rs, l, env)
		if err != nil {
			return nil, nil, err
		}
		pf.Item = surface
		pub.Files[name] = pf
	}
	return pub, ps, nil
}

// blobType resolves a blob's media type from the local cache, else from
// its object name in the private bucket.
func blobType(env Env, l Listing) jobs.TypeOf {
	return func(sum string) (media.Type, bool) {
		if b, ok := env.Blobs.Blob(sum); ok {
			return jobs.ByExt(b.Ext())
		}
		if o, ok := l.Blobs[sum]; ok {
			if _, _, ext, ok := ParseCAS(o.Name); ok {
				return jobs.ByExt(ext)
			}
		}
		return media.Type{}, false
	}
}

// publicFile finds the checksum and size of a file a view links.
func publicFile(name string, rs *receipts, l Listing, env Env) (PublicFile, error) {
	if kind, sum, _, ok := ParseCAS(name); ok && kind == "blobs" {
		if b, ok := env.Blobs.Blob(sum); ok {
			fi, err := os.Stat(b.Path())
			if err != nil {
				return PublicFile{}, err
			}
			return PublicFile{SHA256: sum, Size: fi.Size()}, nil
		}
		if o, ok := l.Blobs[sum]; ok {
			return PublicFile{SHA256: sum, Size: o.Size}, nil
		}
		return PublicFile{}, fmt.Errorf("view links %s, which is in neither the cache nor the private bucket", name)
	}
	key, file, ok := jobs.SplitDerived(name)
	if !ok {
		return PublicFile{}, fmt.Errorf("view links %s, which isn't a blob or a job output", name)
	}
	rc, ok := rs.Receipt(key)
	if !ok {
		return PublicFile{}, fmt.Errorf("view links %s, whose job has no receipt", name)
	}
	for _, f := range rc.Files {
		if f.Name == file {
			return PublicFile{SHA256: f.SHA256, Size: f.Size}, nil
		}
	}
	return PublicFile{}, fmt.Errorf("view links %s, which its job's receipt doesn't list", name)
}

// diffPublic adds the public bucket's ops.
func diffPublic(p *PublicState, pub map[string]gcs.Object, add func(Op)) {
	names := make([]string, 0, len(p.Files))
	for n := range p.Files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f := p.Files[n]
		obj, there := pub[n]
		switch {
		case there && obj.SHA256 == f.SHA256:
		case there:
			add(Op{Type: PublishCopy, Path: n, Bucket: Public, Detail: f.Item + "; replace: checksum mismatch", Bytes: ptr(f.Size)})
		default:
			add(Op{Type: PublishCopy, Path: n, Bucket: Public, Detail: f.Item, Bytes: ptr(f.Size)})
		}
	}
	surfacesSorted := make([]string, 0, len(p.Views))
	for s := range p.Views {
		surfacesSorted = append(surfacesSorted, s)
	}
	sort.Strings(surfacesSorted)
	for _, s := range surfacesSorted {
		b := p.Views[s]
		path := publish.ViewPath(s)
		if obj, ok := pub[path]; !ok || obj.SHA256 != gcs.SHA256Hex(b) {
			add(Op{Type: WriteView, Path: path, Bucket: Public, Detail: s, Bytes: ptr(int64(len(b)))})
		}
	}
	if obj, ok := pub[publish.PublicManifestPath]; !ok || obj.SHA256 != gcs.SHA256Hex(p.Manifest) {
		add(Op{Type: WritePublicManifest, Path: publish.PublicManifestPath, Bucket: Public,
			Detail: fmt.Sprintf("%d public items", p.Items), Bytes: ptr(int64(len(p.Manifest)))})
	}
	known := map[string]bool{publish.PublicManifestPath: true}
	for s := range p.Views {
		known[publish.ViewPath(s)] = true
	}
	stale := make([]string, 0)
	for n := range pub {
		stale = append(stale, n)
	}
	sort.Strings(stale)
	for _, n := range stale {
		obj := pub[n]
		switch {
		case known[n] || p.Files[n] != (PublicFile{}):
		case strings.HasPrefix(n, "blobs/") || strings.HasPrefix(n, jobs.DerivedPrefix):
			add(Op{Type: Unpublish, Path: n, Bucket: Public, Detail: "no longer public", Bytes: ptr(obj.Size)})
		default:
			add(Op{Type: Orphan, Path: n, Bucket: Public, Detail: "not written by Canon", Bytes: ptr(obj.Size)})
		}
	}
}

func sidecarPath(planPath string) string {
	return strings.TrimSuffix(planPath, ".json") + ".public.json"
}

func writeSidecar(planPath string, p *Plan) error {
	if p.public == nil {
		os.Remove(sidecarPath(planPath))
		return nil
	}
	sc := Sidecar{Manifest: string(p.public.Manifest), Views: map[string]string{}, Images: p.public.Images}
	for s, b := range p.public.Views {
		sc.Views[s] = string(b)
	}
	b, err := json.Marshal(sc)
	if err != nil {
		return err
	}
	return os.WriteFile(sidecarPath(planPath), b, 0o600)
}

func readSidecar(planPath string, p *Plan) error {
	b, err := os.ReadFile(sidecarPath(planPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var sc Sidecar
	if err := json.Unmarshal(b, &sc); err != nil {
		return fmt.Errorf("read %s: %w", sidecarPath(planPath), err)
	}
	p.public = &PublicState{Manifest: []byte(sc.Manifest), Views: map[string][]byte{}, Images: sc.Images}
	for s, v := range sc.Views {
		p.public.Views[s] = []byte(v)
	}
	return nil
}

// PublicBytes returns what a plan's write-view or write-public-manifest op
// writes.
func (p *Plan) PublicBytes(path string) ([]byte, bool) {
	if p.public == nil {
		return nil, false
	}
	if path == publish.PublicManifestPath {
		return p.public.Manifest, true
	}
	for s, b := range p.public.Views {
		if publish.ViewPath(s) == path {
			return b, true
		}
	}
	return nil, false
}

// PublicImages maps public item ids to their main image URL.
func (p *Plan) PublicImages() map[string]string {
	if p.public == nil {
		return nil
	}
	return p.public.Images
}

// PreviewView resolves one surface's view from m without publishing
// anything: the projection of m as it stands (what the next apply would
// write), or with drafts every placed item as if it were public. URLs point
// where the files would be published. Job outputs come from the local
// cache, else the private bucket when env has one.
func PreviewView(ctx context.Context, m *manifest.Manifest, env Env, surface string, drafts bool) ([]byte, []manifest.Problem, error) {
	if drafts {
		c := *m
		c.Items = append([]manifest.Item(nil), m.Items...)
		for i := range c.Items {
			c.Items[i].Visibility = manifest.ItemVisibilityPublic
		}
		m = &c
	}
	pm := publish.Project(m)
	l := Listing{Blobs: map[string]gcs.Object{}, Derived: map[string]map[string]gcs.Object{}}
	if env.Remote.API != nil {
		full, err := List(ctx, env.Remote.API, env.Remote.Bucket, env.ArchiveBucket)
		if err != nil {
			return nil, nil, err
		}
		l = full
	}
	rs := &receipts{der: env.Derived, api: env.Remote.API, bucket: env.Remote.Bucket, derived: l.Derived, ctx: ctx, cache: map[string]derived.Receipt{}}
	typeOf := blobType(env, l)
	r, err := views.Resolve(pm, surface, publish.NewJobMedia(pm, typeOf, rs), env.PublicBase)
	if err != nil {
		return nil, nil, err
	}
	b, err := r.Bytes(surfaces.MustDefault())
	return b, r.Problems, err
}
