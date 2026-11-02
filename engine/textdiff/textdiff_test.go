package textdiff

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// apply rebuilds b from a and the hunks: the diff is correct iff it does.
func apply(a string, d Diff) string {
	al := split(a)
	var out []string
	at := 0 // index into al
	for _, h := range d.Hunks {
		for at < h.OldStart-1 {
			out = append(out, al[at])
			at++
		}
		for _, l := range h.Lines {
			switch l.Op {
			case Same:
				out = append(out, al[at])
				at++
			case Del:
				at++
			case Add:
				out = append(out, l.Text)
			}
		}
	}
	out = append(out, al[at:]...)
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n") + "\n"
}

func TestSmall(t *testing.T) {
	a := "{\n  \"a\": 1,\n  \"b\": 2,\n  \"c\": 3\n}\n"
	b := "{\n  \"a\": 1,\n  \"b\": 20,\n  \"c\": 3,\n  \"d\": 4\n}\n"
	d := Compute(a, b, 1)
	if d.Added != 3 || d.Removed != 2 || len(d.Hunks) != 1 || d.Rough {
		t.Fatalf("%+v", d)
	}
	if got := apply(a, d); got != b {
		t.Fatalf("apply:\n%s", got)
	}
	if d := Compute(a, a, 3); len(d.Hunks) != 0 || d.Added+d.Removed != 0 {
		t.Errorf("same text: %+v", d)
	}
	if d := Compute("", b, 3); d.Added != 6 || apply("", d) != b {
		t.Errorf("from nothing: %+v", d)
	}
}

func TestRandomRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for round := 0; round < 300; round++ {
		var a []string
		for i := 0; i < rng.Intn(80); i++ {
			a = append(a, fmt.Sprint(rng.Intn(10)))
		}
		b := append([]string(nil), a...)
		for e := 0; e < rng.Intn(10); e++ {
			i := rng.Intn(len(b) + 1)
			switch rng.Intn(3) {
			case 0:
				b = append(b[:i], append([]string{"new"}, b[i:]...)...)
			case 1:
				if i < len(b) {
					b = append(b[:i], b[i+1:]...)
				}
			default:
				if i < len(b) {
					b[i] = "changed"
				}
			}
		}
		as, bs := join(a), join(b)
		d := Compute(as, bs, 2)
		if got := apply(as, d); got != bs {
			t.Fatalf("round %d:\na %q\nb %q\ngot %q", round, as, bs, got)
		}
	}
}

func TestHunksSeparate(t *testing.T) {
	var a []string
	for i := 0; i < 50; i++ {
		a = append(a, fmt.Sprint(i))
	}
	b := append([]string(nil), a...)
	b[5], b[40] = "x", "y"
	d := Compute(join(a), join(b), 3)
	if len(d.Hunks) != 2 || d.Hunks[1].OldStart != 38 {
		t.Fatalf("%+v", d.Hunks)
	}
}

func join(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return strings.Join(s, "\n") + "\n"
}
