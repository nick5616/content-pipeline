// Package gcs builds Canon's Cloud Storage client. Credentials come from ADC,
// optionally impersonating the configured service account. Setting
// STORAGE_EMULATOR_HOST (fake-gcs-server in tests) skips auth entirely.
package gcs

import (
	"context"
	"errors"
	"fmt"
	"os"

	"cloud.google.com/go/storage"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/impersonate"
	"google.golang.org/api/option"

	"github.com/nick5616/canon/engine/config"
)

// Scope is the only OAuth scope Canon asks for.
const Scope = storage.ScopeFullControl

// API is the slice of GCS that callers depend on, so tests can fake it.
// It grows as later milestones need more.
type API interface {
	BucketAttrs(ctx context.Context, bucket string) (*storage.BucketAttrs, error)
	Close() error
}

// Client wraps *storage.Client to satisfy API.
type Client struct{ *storage.Client }

func (c Client) BucketAttrs(ctx context.Context, bucket string) (*storage.BucketAttrs, error) {
	return c.Bucket(bucket).Attrs(ctx)
}

// NewClient returns a client authenticated by ADC, impersonating
// cfg.ImpersonateServiceAccount when it is set.
func NewClient(ctx context.Context, cfg config.Config) (Client, error) {
	var opts []option.ClientOption
	if os.Getenv("STORAGE_EMULATOR_HOST") == "" {
		ts, err := TokenSource(ctx, cfg)
		if err != nil {
			return Client{}, err
		}
		opts = append(opts, option.WithTokenSource(ts))
	}
	c, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return Client{}, fmt.Errorf("gcs client: %w", err)
	}
	return Client{c}, nil
}

// ErrNoADC means Application Default Credentials are not set up.
var ErrNoADC = errors.New("no Application Default Credentials; run `gcloud auth application-default login`")

// TokenSource returns ADC, or an impersonated source when the config names a
// service account. It does not mint a token; call Token to prove it works.
func TokenSource(ctx context.Context, cfg config.Config) (oauth2.TokenSource, error) {
	creds, err := google.FindDefaultCredentials(ctx, Scope)
	if err != nil {
		return nil, fmt.Errorf("%w (%v)", ErrNoADC, err)
	}
	if cfg.ImpersonateServiceAccount == "" {
		return creds.TokenSource, nil
	}
	ts, err := impersonate.CredentialsTokenSource(ctx, impersonate.CredentialsConfig{
		TargetPrincipal: cfg.ImpersonateServiceAccount,
		Scopes:          []string{Scope},
	}, option.WithCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("impersonate %s: %w", cfg.ImpersonateServiceAccount, err)
	}
	return ts, nil
}
