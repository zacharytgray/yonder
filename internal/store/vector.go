package store

import (
	"fmt"
	"sort"
	"strings"
)

// Vector is a version counter per machine. a file's vector grows by one on
// the machine that changes it.
type Vector map[string]uint64

// Order is how two vectors relate.
type Order int

const (
	Equal      Order = iota
	Before           // the other has seen everything this one has, and more
	After            // this one has seen everything the other has, and more
	Concurrent       // each has a change the other hasn't seen
)

// Compare says how v relates to o.
func (v Vector) Compare(o Vector) Order {
	ahead, behind := false, false
	for k, n := range v {
		switch m := o[k]; {
		case n > m:
			ahead = true
		case n < m:
			behind = true
		}
	}
	for k, m := range o {
		if _, ok := v[k]; !ok && m > 0 {
			behind = true
		}
	}
	switch {
	case ahead && behind:
		return Concurrent
	case ahead:
		return After
	case behind:
		return Before
	}
	return Equal
}

// Dominates: v has seen everything o has (equal counts).
func (v Vector) Dominates(o Vector) bool {
	c := v.Compare(o)
	return c == After || c == Equal
}

// Max is the counter-wise maximum.
func Max(a, b Vector) Vector {
	out := Vector{}
	for k, n := range a {
		out[k] = n
	}
	for k, n := range b {
		if n > out[k] {
			out[k] = n
		}
	}
	return out
}

// Bump is a copy of v with self's counter one higher.
func (v Vector) Bump(self string) Vector {
	out := Max(v, nil)
	out[self]++
	return out
}

func (v Vector) String() string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s:%d", k, v[k])
	}
	return "{" + strings.Join(parts, " ") + "}"
}
