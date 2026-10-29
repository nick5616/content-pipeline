package gcs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"maps"
	"sort"
	"strings"
	"sync"

	"cloud.google.com/go/storage"
)

// Mem is an in-memory API for tests: generations, preconditions, CRC32C
// checks, and fault injection. Use fake-gcs-server for the real client.
type Mem struct {
	mu      sync.Mutex
	buckets map[string]map[string]*memObject
	gen     int64

	// Fault, if set, runs before every call. A non-nil error fails the call.
	// For writes, a *Cut makes the upload fail partway instead.
	Fault func(call Call) error
	// Calls records every call, in order.
	Calls []Call
}

// Call is one API call, as seen by Mem.Fault.
type Call struct{ Method, Bucket, Name string }

// Cut makes a Mem write read After bytes, then fail with Err, storing
// nothing (GCS only creates an object when an upload finishes).
type Cut struct {
	After int64
	Err   error
}

func (c *Cut) Error() string { return fmt.Sprintf("upload cut after %d bytes: %v", c.After, c.Err) }

type memObject struct {
	data []byte
	gen  int64
	meta map[string]string
	ct   string
	cc   string
}

// NewMem returns a fake with the given buckets.
func NewMem(buckets ...string) *Mem {
	m := &Mem{buckets: map[string]map[string]*memObject{}}
	for _, b := range buckets {
		m.buckets[b] = map[string]*memObject{}
	}
	return m
}

func (m *Mem) call(method, bucket, name string) (map[string]*memObject, error) {
	c := Call{method, bucket, name}
	m.mu.Lock()
	m.Calls = append(m.Calls, c)
	fault := m.Fault
	m.mu.Unlock()
	if fault != nil {
		if err := fault(c); err != nil {
			return nil, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.buckets[bucket]
	if !ok {
		return nil, storage.ErrBucketNotExist
	}
	return b, nil
}

func (m *Mem) BucketAttrs(_ context.Context, bucket string) (*storage.BucketAttrs, error) {
	if _, err := m.call("bucketAttrs", bucket, ""); err != nil {
		return nil, err
	}
	return &storage.BucketAttrs{Name: bucket}, nil
}

func (m *Mem) List(_ context.Context, bucket, prefix string) ([]Object, error) {
	b, err := m.call("list", bucket, prefix)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Object
	for name, o := range b {
		if strings.HasPrefix(name, prefix) {
			out = append(out, o.object(name))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Mem) Attrs(_ context.Context, bucket, name string) (Object, error) {
	b, err := m.call("attrs", bucket, name)
	if err != nil {
		return Object{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := b[name]
	if !ok {
		return Object{}, ErrNotExist
	}
	return o.object(name), nil
}

func (m *Mem) Read(_ context.Context, bucket, name string) ([]byte, int64, error) {
	b, err := m.call("read", bucket, name)
	if err != nil {
		return nil, 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := b[name]
	if !ok {
		return nil, 0, ErrNotExist
	}
	return bytes.Clone(o.data), o.gen, nil
}

func (m *Mem) Write(ctx context.Context, bucket, name string, r io.Reader, opts WriteOpts) (Object, error) {
	b, err := m.call("write", bucket, name)
	var cut *Cut
	if errors.As(err, &cut) {
		_, _ = io.CopyN(io.Discard, r, cut.After)
		return Object{}, cut
	}
	if err != nil {
		return Object{}, err
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return Object{}, err
	}
	if err := ctx.Err(); err != nil {
		return Object{}, err
	}
	if crc32.Checksum(data, CRC32C) != opts.CRC32C {
		return Object{}, fmt.Errorf("crc32c mismatch for %s", name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, exists := b[name]
	if (opts.IfGeneration == 0 && exists) || (opts.IfGeneration != 0 && (!exists || cur.gen != opts.IfGeneration)) {
		return Object{}, ErrPrecondition
	}
	m.gen++
	o := &memObject{data: data, gen: m.gen, meta: map[string]string{MetaSHA256: opts.SHA256}, ct: opts.ContentType, cc: opts.CacheControl}
	b[name] = o
	return o.object(name), nil
}

func (m *Mem) Close() error { return nil }

func (o *memObject) object(name string) Object {
	return Object{Name: name, Size: int64(len(o.data)), Generation: o.gen, SHA256: o.meta[MetaSHA256], Metadata: maps.Clone(o.meta)}
}

// Put stores an object directly, bypassing faults and preconditions, as if
// someone else wrote it. sha256 is the metadata to set ("" for none).
func (m *Mem) Put(bucket, name string, data []byte, sha string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gen++
	m.buckets[bucket][name] = &memObject{data: bytes.Clone(data), gen: m.gen, meta: map[string]string{MetaSHA256: sha}}
	return m.gen
}

// PutMeta stores an object with arbitrary custom metadata, like one another
// tool wrote.
func (m *Mem) PutMeta(bucket, name string, data []byte, meta map[string]string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gen++
	m.buckets[bucket][name] = &memObject{data: bytes.Clone(data), gen: m.gen, meta: maps.Clone(meta)}
	return m.gen
}

// Get returns an object's bytes, content type and cache control, for
// assertions.
func (m *Mem) Get(bucket, name string) (data []byte, contentType, cacheControl string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.buckets[bucket][name]
	if !ok {
		return nil, "", "", false
	}
	return bytes.Clone(o.data), o.ct, o.cc, true
}

// Names lists every object in a bucket, sorted.
func (m *Mem) Names(bucket string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for n := range m.buckets[bucket] {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// SHA256Hex is a helper for tests and callers: the hex SHA-256 of b.
func SHA256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
