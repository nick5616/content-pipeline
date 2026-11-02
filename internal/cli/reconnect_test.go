package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/nick5616/canon/engine/gcs"
)

// A client stuck on a dead connection fails with ErrOffline; the next call
// gets a fresh client, and the error carries its cause.
func TestReconnectingAfterOffline(t *testing.T) {
	made := 0
	var first *gcs.Mem
	r := &reconnecting{make: func(context.Context) (gcs.API, error) {
		made++
		m := gcs.NewMem("b")
		m.Put("b", "x", []byte("hi"), "")
		if made == 1 {
			first = m
			m.Fault = func(gcs.Call) error { return errors.Join(gcs.ErrOffline, errors.New("read tcp: connection reset")) }
		}
		return m, nil
	}}
	ctx := context.Background()
	_, err := r.Attrs(ctx, "b", "x")
	if !errors.Is(err, gcs.ErrOffline) || made != 1 {
		t.Fatalf("first call: %v (made %d)", err, made)
	}
	if _, err := r.Attrs(ctx, "b", "x"); err != nil || made != 2 {
		t.Fatalf("second call: %v (made %d)", err, made)
	}
	// Other errors keep the client.
	if _, err := r.Attrs(ctx, "b", "missing"); !errors.Is(err, gcs.ErrNotExist) || made != 2 {
		t.Fatalf("not-exist: %v (made %d)", err, made)
	}
	_ = first
}
