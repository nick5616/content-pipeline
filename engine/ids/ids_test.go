package ids

import (
	"math/rand"
	"regexp"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
)

var fixed = func() time.Time { return time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC) }

func seq(n int) []string {
	g := NewWith(fixed, rand.New(rand.NewSource(42)))
	out := make([]string, n)
	for i := range out {
		out[i] = g.New().String()
	}
	return out
}

func TestDeterministic(t *testing.T) {
	a, b := seq(5), seq(5)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("id %d differs: %s vs %s", i, a[i], b[i])
		}
	}
	if got := ulid.Time(ulid.MustParse(a[0]).Time()); !got.Equal(fixed()) {
		t.Fatalf("timestamp decodes to %v", got)
	}
}

func TestMonotonicWithinMillisecond(t *testing.T) {
	ids := seq(100)
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("not increasing at %d: %s <= %s", i, ids[i], ids[i-1])
		}
	}
}

func TestNewMatchesSchemaPattern(t *testing.T) {
	re := regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	for range 10 {
		if id := New().String(); !re.MatchString(id) {
			t.Fatalf("%s does not match the schema's ulid pattern", id)
		}
	}
}
