package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/nick5616/canon/engine/gcs"
	"github.com/nick5616/canon/engine/plan"
	"github.com/nick5616/canon/engine/store"
	"github.com/nick5616/canon/engine/textdiff"
)

// FileDiff is what one of a plan's manifest or view writes changes
// (openapi FileDiff): the object now, against the bytes the plan writes.
type FileDiff struct {
	Path   string `json:"path"`
	Bucket string `json:"bucket"`
	// Exists is whether the object is in the bucket now (false: all added).
	Exists bool `json:"exists"`
	textdiff.Diff
}

// ErrNotDiffable means the path isn't a manifest or view write in the plan.
var ErrNotDiffable = errors.New("not a manifest or view this plan writes")

// diffable are the ops whose bytes are JSON documents worth diffing.
var diffable = map[plan.OpType]bool{plan.WriteManifest: true, plan.WriteView: true, plan.WritePublicManifest: true}

// DiffablePaths lists the plan's manifest and view writes, in plan order.
func DiffablePaths(p *plan.Plan) []plan.Op {
	var out []plan.Op
	for _, o := range p.Ops {
		if diffable[o.Type] {
			out = append(out, o)
		}
	}
	return out
}

// PlanDiff compares what the plan writes at path with what's there now.
func (e *Engine) PlanDiff(ctx context.Context, p *plan.Plan, path string) (FileDiff, error) {
	var op *plan.Op
	for i := range p.Ops {
		if p.Ops[i].Path == path && diffable[p.Ops[i].Type] {
			op = &p.Ops[i]
		}
	}
	if op == nil {
		return FileDiff{}, fmt.Errorf("%w: %s", ErrNotDiffable, path)
	}
	var after []byte
	bucket := e.Config.PublicBucket
	if op.Type == plan.WriteManifest {
		b, _, _, err := plan.Current(e.Working)
		if err != nil {
			return FileDiff{}, err
		}
		if store.Revision(b) != p.Revision {
			return FileDiff{}, fmt.Errorf("%w: the working manifest was edited since this plan was made", store.ErrStale)
		}
		after, bucket = b, e.Config.PrivateBucket
	} else {
		b, ok := p.PublicBytes(path)
		if !ok {
			return FileDiff{}, fmt.Errorf("%w: the plan's bytes for %s are missing", ErrNotDiffable, path)
		}
		after = b
	}
	api, err := e.GCS(ctx)
	if err != nil {
		return FileDiff{}, err
	}
	before, _, err := api.Read(ctx, bucket, path)
	exists := err == nil
	if err != nil && !errors.Is(err, gcs.ErrNotExist) {
		return FileDiff{}, err
	}
	return FileDiff{Path: path, Bucket: string(op.Bucket), Exists: exists, Diff: textdiff.Compute(string(before), string(after), 3)}, nil
}

func (a *engineAPI) planDiff(w http.ResponseWriter, r *http.Request) {
	p, err := plan.Open(a.e.StateDir, r.PathValue("id"))
	if err != nil {
		engineError(w, err)
		return
	}
	d, err := a.e.PlanDiff(r.Context(), p, r.URL.Query().Get("path"))
	switch {
	case errors.Is(err, ErrNotDiffable):
		problem(w, http.StatusNotFound, "Nothing to diff", err.Error())
	case errors.Is(err, store.ErrStale):
		problem(w, http.StatusConflict, "Plan is stale", err.Error())
	case err != nil:
		engineError(w, err)
	default:
		writeJSON(w, http.StatusOK, d)
	}
}
