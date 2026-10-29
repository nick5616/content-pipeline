package gcs

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"net/url"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
)

// API is the slice of GCS that callers depend on, so tests can fake it (Mem).
type API interface {
	BucketAttrs(ctx context.Context, bucket string) (*storage.BucketAttrs, error)
	// List returns every object under prefix, sorted by name. No downloads.
	List(ctx context.Context, bucket, prefix string) ([]Object, error)
	// Attrs returns one object's metadata, or ErrNotExist.
	Attrs(ctx context.Context, bucket, name string) (Object, error)
	// Read downloads an object and returns its bytes and generation.
	Read(ctx context.Context, bucket, name string) ([]byte, int64, error)
	// Write uploads r. Every write is conditional (see WriteOpts.IfGeneration),
	// which also makes it safe to retry.
	Write(ctx context.Context, bucket, name string, r io.Reader, o WriteOpts) (Object, error)
	Close() error
}

// Object is an object's metadata as Canon uses it.
type Object struct {
	Name       string
	Size       int64
	Generation int64
	// SHA256 is the x-goog-meta-sha256 metadata Canon sets on every object;
	// empty if someone else wrote it.
	SHA256 string
}

// MetaSHA256 is the custom metadata key holding an object's SHA-256.
const MetaSHA256 = "sha256"

// WriteOpts describe one upload.
type WriteOpts struct {
	ContentType  string
	CacheControl string
	SHA256       string // stored as x-goog-meta-sha256
	// CRC32C of the bytes (Castagnoli), checked by GCS on upload.
	CRC32C uint32
	// IfGeneration is the generation the object must have now; 0 means it
	// must not exist. There is no unconditional write.
	IfGeneration int64
}

var (
	// ErrNotExist means the object isn't there.
	ErrNotExist = errors.New("object does not exist")
	// ErrPrecondition means IfGeneration didn't match.
	ErrPrecondition = errors.New("precondition failed: the object changed")
	// ErrOffline means GCS couldn't be reached at all.
	ErrOffline = errors.New("GCS is unreachable")
)

// CRC32C is the checksum table GCS uses.
var CRC32C = crc32.MakeTable(crc32.Castagnoli)

func (c Client) List(ctx context.Context, bucket, prefix string) ([]Object, error) {
	q := &storage.Query{Prefix: prefix}
	if err := q.SetAttrSelection([]string{"Name", "Size", "Generation", "Metadata"}); err != nil {
		return nil, err
	}
	var out []Object
	it := c.Bucket(bucket).Objects(ctx, q)
	for {
		a, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, classify(err)
		}
		out = append(out, object(a))
	}
}

func (c Client) Attrs(ctx context.Context, bucket, name string) (Object, error) {
	a, err := c.Bucket(bucket).Object(name).Attrs(ctx)
	if err != nil {
		return Object{}, classify(err)
	}
	return object(a), nil
}

func (c Client) Read(ctx context.Context, bucket, name string) ([]byte, int64, error) {
	r, err := c.Bucket(bucket).Object(name).NewReader(ctx)
	if err != nil {
		return nil, 0, classify(err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, 0, classify(err)
	}
	return b, r.Attrs.Generation, nil
}

func (c Client) Write(ctx context.Context, bucket, name string, r io.Reader, o WriteOpts) (Object, error) {
	cond := storage.Conditions{GenerationMatch: o.IfGeneration}
	if o.IfGeneration == 0 {
		cond = storage.Conditions{DoesNotExist: true}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel() // abandons the upload if we return early
	w := c.Bucket(bucket).Object(name).If(cond).NewWriter(ctx)
	w.ContentType = o.ContentType
	w.CacheControl = o.CacheControl
	w.Metadata = map[string]string{MetaSHA256: o.SHA256}
	w.CRC32C = o.CRC32C
	w.SendCRC32C = true
	if _, err := io.Copy(w, r); err != nil {
		cancel()
		_ = w.Close()
		return Object{}, classify(err)
	}
	if err := w.Close(); err != nil {
		return Object{}, classify(err)
	}
	return object(w.Attrs()), nil
}

func object(a *storage.ObjectAttrs) Object {
	return Object{Name: a.Name, Size: a.Size, Generation: a.Generation, SHA256: a.Metadata[MetaSHA256]}
}

// classify maps storage errors onto ErrNotExist, ErrPrecondition and
// ErrOffline, keeping the original error in the chain.
func classify(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, storage.ErrObjectNotExist) {
		return fmt.Errorf("%w: %v", ErrNotExist, err)
	}
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		switch gerr.Code {
		case 404:
			return fmt.Errorf("%w: %v", ErrNotExist, err)
		case 412:
			return fmt.Errorf("%w: %v", ErrPrecondition, err)
		}
		return err
	}
	var nerr net.Error
	var uerr *url.Error
	var operr *net.OpError
	if errors.As(err, &operr) || errors.As(err, &uerr) || errors.As(err, &nerr) {
		return fmt.Errorf("%w: %v", ErrOffline, err)
	}
	return err
}
