package cli

import (
	"context"
	"errors"
	"io"
	"sync"

	"cloud.google.com/go/storage"

	"github.com/nick5616/canon/engine/gcs"
)

// reconnecting is the GCS client canon serve keeps for its lifetime. A
// long-lived client can get stuck on a connection that died while the
// machine slept or changed networks (HTTP/2 keeps reusing it), while a new
// process works fine. So after any "unreachable" error the client is
// dropped, and the next call makes a fresh one.
type reconnecting struct {
	make func(context.Context) (gcs.API, error)

	mu  sync.Mutex
	cur gcs.API
}

func (r *reconnecting) get(ctx context.Context) (gcs.API, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur == nil {
		c, err := r.make(ctx)
		if err != nil {
			return nil, err
		}
		r.cur = c
	}
	return r.cur, nil
}

// check drops the client after an unreachable error.
func (r *reconnecting) check(c gcs.API, err error) error {
	if errors.Is(err, gcs.ErrOffline) {
		r.mu.Lock()
		if r.cur == c {
			r.cur = nil
			go c.Close()
		}
		r.mu.Unlock()
	}
	return err
}

func (r *reconnecting) BucketAttrs(ctx context.Context, bucket string) (*storage.BucketAttrs, error) {
	c, err := r.get(ctx)
	if err != nil {
		return nil, err
	}
	a, err := c.BucketAttrs(ctx, bucket)
	return a, r.check(c, err)
}

func (r *reconnecting) List(ctx context.Context, bucket, prefix string) ([]gcs.Object, error) {
	c, err := r.get(ctx)
	if err != nil {
		return nil, err
	}
	o, err := c.List(ctx, bucket, prefix)
	return o, r.check(c, err)
}

func (r *reconnecting) Attrs(ctx context.Context, bucket, name string) (gcs.Object, error) {
	c, err := r.get(ctx)
	if err != nil {
		return gcs.Object{}, err
	}
	o, err := c.Attrs(ctx, bucket, name)
	return o, r.check(c, err)
}

func (r *reconnecting) Read(ctx context.Context, bucket, name string) ([]byte, int64, error) {
	c, err := r.get(ctx)
	if err != nil {
		return nil, 0, err
	}
	b, g, err := c.Read(ctx, bucket, name)
	return b, g, r.check(c, err)
}

func (r *reconnecting) Write(ctx context.Context, bucket, name string, body io.Reader, o gcs.WriteOpts) (gcs.Object, error) {
	c, err := r.get(ctx)
	if err != nil {
		return gcs.Object{}, err
	}
	obj, err := c.Write(ctx, bucket, name, body, o)
	return obj, r.check(c, err)
}

func (r *reconnecting) Copy(ctx context.Context, srcBucket, src, dstBucket, dst string, o gcs.CopyOpts) (gcs.Object, error) {
	c, err := r.get(ctx)
	if err != nil {
		return gcs.Object{}, err
	}
	obj, err := c.Copy(ctx, srcBucket, src, dstBucket, dst, o)
	return obj, r.check(c, err)
}

func (r *reconnecting) Delete(ctx context.Context, bucket, name string, gen int64) error {
	c, err := r.get(ctx)
	if err != nil {
		return err
	}
	return r.check(c, c.Delete(ctx, bucket, name, gen))
}

func (r *reconnecting) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur == nil {
		return nil
	}
	err := r.cur.Close()
	r.cur = nil
	return err
}
