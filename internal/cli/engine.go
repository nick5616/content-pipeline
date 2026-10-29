package cli

import (
	"context"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/nick5616/canon/api"
	"github.com/nick5616/canon/engine/blobstore"
	"github.com/nick5616/canon/engine/config"
	"github.com/nick5616/canon/engine/derived"
	"github.com/nick5616/canon/engine/gcs"
	"github.com/nick5616/canon/engine/jobs"
	"github.com/nick5616/canon/engine/recipes"
	"github.com/nick5616/canon/engine/store"
	"github.com/nick5616/canon/engine/surfaces"
)

// newGCS makes the GCS client. Tests replace it with a fake.
var newGCS = func(ctx context.Context, cfg config.Config) (gcs.API, error) {
	c, err := gcs.NewClient(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// newTools runs recipe tools. Tests replace it.
var newTools = recipes.NewTools

// newEngine wires the engine from config. The GCS client is made on first
// use, so commands that only edit locally never need credentials.
func newEngine(cfg config.Config) (*api.Engine, error) {
	reg, err := surfaces.Default()
	if err != nil {
		return nil, err
	}
	blobs, err := blobstore.Open(cfg.CacheDir)
	if err != nil {
		return nil, err
	}
	der, err := derived.Open(cfg.CacheDir)
	if err != nil {
		return nil, err
	}
	db, err := jobs.OpenDB(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	var (
		once   sync.Once
		client gcs.API
		cerr   error
	)
	return &api.Engine{
		Derived:  der,
		Jobs:     db,
		Tools:    newTools(),
		Config:   cfg,
		Working:  store.Open(cfg.StateDir, cfg.ProfileName, reg),
		Blobs:    blobs,
		StateDir: cfg.StateDir,
		Now:      time.Now,
		GCS: func(ctx context.Context) (gcs.API, error) {
			once.Do(func() { client, cerr = newGCS(ctx, cfg) })
			return client, cerr
		},
	}, nil
}

// engineFor loads config (a missing file means defaults) and builds the
// engine for a command.
func engineFor(cmd *cobra.Command) (*api.Engine, error) {
	cfg, err := ingestConfig(cmd)
	if err != nil {
		return nil, err
	}
	return newEngine(cfg)
}
