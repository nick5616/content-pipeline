package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"

	"github.com/nick5616/canon/engine/blobstore"
	"github.com/nick5616/canon/engine/ids"
	"github.com/nick5616/canon/engine/ingest"
)

// batch is one upload's progress. Batches live in memory: a restart forgets
// them, but the blobs and drafts they made are on disk.
type batch struct {
	ID    string              `json:"id"`
	Files []ingest.FileResult `json:"files"`
}

type ingestAPI struct {
	in *ingest.Ingester

	mu      sync.Mutex
	batches map[string]*batch
}

func newIngestAPI(in *ingest.Ingester) *ingestAPI {
	return &ingestAPI{in: in, batches: map[string]*batch{}}
}

// start stages every uploaded file while the request is open (the body is
// only readable then), answers 202, and processes the batch in the
// background.
func (a *ingestAPI) start(w http.ResponseWriter, r *http.Request) {
	if a.in == nil {
		problem(w, http.StatusServiceUnavailable, "Ingest unavailable", "canon serve was started without a cache.")
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		problem(w, http.StatusBadRequest, "Expected multipart/form-data", err.Error())
		return
	}
	var staged []blobstore.Original
	discard := func() {
		for _, o := range staged {
			ingest.Discard(o)
		}
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			discard()
			problem(w, http.StatusBadRequest, "Bad multipart body", err.Error())
			return
		}
		if part.FormName() != "files" {
			part.Close()
			continue
		}
		// Browsers send base names; strip any path anyway.
		name := path.Base(strings.ReplaceAll(part.FileName(), `\`, "/"))
		if name == "." || name == "/" {
			name = "upload"
		}
		o, err := a.in.StageReader(part, name)
		part.Close()
		if err != nil {
			discard()
			if errors.Is(err, blobstore.ErrTooLarge) {
				problem(w, http.StatusRequestEntityTooLarge, "File too large", err.Error())
				return
			}
			problem(w, http.StatusInternalServerError, "Could not stage upload", err.Error())
			return
		}
		staged = append(staged, o)
	}
	if len(staged) == 0 {
		problem(w, http.StatusBadRequest, "No files", `Send one or more parts named "files".`)
		return
	}

	b := &batch{ID: ids.New().String()}
	for _, o := range staged {
		b.Files = append(b.Files, ingest.FileResult{Name: o.Name(), Size: o.Size(), Stage: ingest.Queued})
	}
	a.mu.Lock()
	a.batches[b.ID] = b
	snapshot := a.copyLocked(b)
	a.mu.Unlock()

	go func() {
		for i, o := range staged {
			res := a.in.Process(context.Background(), o, ingest.Options{}, func(fr ingest.FileResult) {
				a.set(b, i, fr)
			})
			a.set(b, i, res)
		}
	}()

	w.Header().Set("Location", "/api/ingest/"+b.ID)
	writeJSON(w, http.StatusAccepted, snapshot)
}

func (a *ingestAPI) get(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	b, ok := a.batches[r.PathValue("id")]
	var snapshot batch
	if ok {
		snapshot = a.copyLocked(b)
	}
	a.mu.Unlock()
	if !ok {
		problem(w, http.StatusNotFound, "No such ingest batch", "Batches are kept until canon serve restarts.")
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (a *ingestAPI) set(b *batch, i int, fr ingest.FileResult) {
	a.mu.Lock()
	b.Files[i] = fr
	a.mu.Unlock()
}

func (a *ingestAPI) copyLocked(b *batch) batch {
	return batch{ID: b.ID, Files: append([]ingest.FileResult{}, b.Files...)}
}
