// Package textdiff diffs two texts by line, for showing what a plan writes:
// canonical JSON (sorted keys, one value per line), so a line diff reads
// well. Common prefix and suffix are trimmed first; the middle is diffed
// with Myers' algorithm. Edits too large to diff in reasonable memory are
// reported as one replacement.
package textdiff

import (
	"strings"
)

// Op is a line's fate: " ", "-" or "+", as in a unified diff.
type Op string

const (
	Same Op = " "
	Del  Op = "-"
	Add  Op = "+"
)

// Line is one line of a hunk.
type Line struct {
	Op   Op     `json:"op"`
	Text string `json:"text"`
}

// Hunk is a run of changes with context, unified-diff style (1-based).
type Hunk struct {
	OldStart int    `json:"oldStart"`
	OldLines int    `json:"oldLines"`
	NewStart int    `json:"newStart"`
	NewLines int    `json:"newLines"`
	Lines    []Line `json:"lines"`
}

// Diff is a whole comparison.
type Diff struct {
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Hunks   []Hunk `json:"hunks"`
	// Rough means the texts differed too much to align, so the changed
	// middle is shown as all removed then all added.
	Rough bool `json:"rough"`
}

// maxD bounds Myers' search (memory grows with its square).
const maxD = 3000

// Compute diffs a and b with context lines around each change.
func Compute(a, b string, context int) Diff {
	al, bl := split(a), split(b)
	ops, rough := lines(al, bl)
	d := Diff{Hunks: []Hunk{}, Rough: rough}
	for _, o := range ops {
		switch o.Op {
		case Add:
			d.Added++
		case Del:
			d.Removed++
		}
	}
	d.Hunks = hunks(ops, context)
	return d
}

func split(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// lines returns the edit script from a to b.
func lines(a, b []string) ([]Line, bool) {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	var out []Line
	for _, s := range a[:pre] {
		out = append(out, Line{Same, s})
	}
	mid, rough := myers(a[pre:len(a)-suf], b[pre:len(b)-suf])
	out = append(out, mid...)
	for _, s := range a[len(a)-suf:] {
		out = append(out, Line{Same, s})
	}
	return out, rough
}

// myers is the O(ND) greedy diff, keeping each step's frontier to trace
// the path back.
func myers(a, b []string) ([]Line, bool) {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		return replace(a, b), false
	}
	max := n + m
	if max > 2*maxD {
		max = 2 * maxD
	}
	off := max
	v := make([]int, 2*max+2)
	var trace [][]int
	for d := 0; d <= max; d++ {
		trace = append(trace, append([]int(nil), v...))
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				return backtrack(a, b, trace, off, d), false
			}
		}
	}
	return replace(a, b), true
}

func backtrack(a, b []string, trace [][]int, off, d int) []Line {
	x, y := len(a), len(b)
	var rev []Line
	for ; d > 0; d-- {
		v := trace[d]
		k := x - y
		var pk int
		if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
			pk = k + 1
		} else {
			pk = k - 1
		}
		px := v[off+pk]
		py := px - pk
		for x > px && y > py {
			x--
			y--
			rev = append(rev, Line{Same, a[x]})
		}
		if x == px {
			y--
			rev = append(rev, Line{Add, b[y]})
		} else {
			x--
			rev = append(rev, Line{Del, a[x]})
		}
	}
	for x > 0 && y > 0 {
		x--
		y--
		rev = append(rev, Line{Same, a[x]})
	}
	out := make([]Line, len(rev))
	for i, l := range rev {
		out[len(rev)-1-i] = l
	}
	return out
}

func replace(a, b []string) []Line {
	var out []Line
	for _, s := range a {
		out = append(out, Line{Del, s})
	}
	for _, s := range b {
		out = append(out, Line{Add, s})
	}
	return out
}

// hunks groups changes with context lines; nearby changes share a hunk.
func hunks(ops []Line, context int) []Hunk {
	var out []Hunk
	oldN, newN := make([]int, len(ops)), make([]int, len(ops))
	o, n := 1, 1
	for i, l := range ops {
		oldN[i], newN[i] = o, n
		if l.Op != Add {
			o++
		}
		if l.Op != Del {
			n++
		}
	}
	for i := 0; i < len(ops); {
		if ops[i].Op == Same {
			i++
			continue
		}
		start := max(0, i-context)
		end := i
		// Extend while the next change is within 2*context lines.
		for j := i; j < len(ops); j++ {
			if ops[j].Op != Same {
				end = j
			} else if j-end > 2*context {
				break
			}
		}
		stop := min(len(ops), end+context+1)
		h := Hunk{OldStart: oldN[start], NewStart: newN[start], Lines: ops[start:stop]}
		for _, l := range h.Lines {
			if l.Op != Add {
				h.OldLines++
			}
			if l.Op != Del {
				h.NewLines++
			}
		}
		out = append(out, h)
		i = stop
	}
	if out == nil {
		out = []Hunk{}
	}
	return out
}
