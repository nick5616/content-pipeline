// Package ids mints item ids: ULIDs, which sort by creation time and never
// change once assigned.
package ids

import (
	"crypto/rand"
	"io"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// Generator mints ULIDs that increase strictly, even within one millisecond.
type Generator struct {
	mu      sync.Mutex
	clock   func() time.Time
	entropy *ulid.MonotonicEntropy
}

// NewWith returns a generator with the given clock and entropy source. Tests
// pass a fixed clock and a fixed reader to get deterministic ids.
func NewWith(clock func() time.Time, entropy io.Reader) *Generator {
	return &Generator{clock: clock, entropy: ulid.Monotonic(entropy, 0)}
}

var defaultGen = NewWith(time.Now, rand.Reader)

// New returns a fresh ULID from the wall clock and crypto/rand.
func New() ulid.ULID { return defaultGen.New() }

// New returns the next ULID.
func (g *Generator) New() ulid.ULID {
	g.mu.Lock()
	defer g.mu.Unlock()
	return ulid.MustNew(ulid.Timestamp(g.clock()), g.entropy)
}
