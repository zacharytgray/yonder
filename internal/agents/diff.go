package agents

import (
	"bytes"
	"fmt"
	"strings"
)

// past this many lines on a side, Diff only says the files differ
const diffMaxLines = 4000

// Diff is a plain line diff of a and b: "-" lines only in a, "+" only in b,
// with up to two lines of context around each change.
func Diff(a, b []byte, nameA, nameB string) string {
	if bytes.Equal(a, b) {
		return ""
	}
	if bytes.IndexByte(a, 0) >= 0 || bytes.IndexByte(b, 0) >= 0 {
		return fmt.Sprintf("binary files %s and %s differ\n", nameA, nameB)
	}
	la, lb := lines(a), lines(b)
	if len(la) > diffMaxLines || len(lb) > diffMaxLines {
		return fmt.Sprintf("%s and %s differ (too long to show; %d and %d lines)\n", nameA, nameB, len(la), len(lb))
	}
	// longest common subsequence, then walk it
	n, m := len(la), len(lb)
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if la[i] == lb[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	type op struct {
		kind byte
		text string
	}
	var ops []op
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && la[i] == lb[j]:
			ops = append(ops, op{' ', la[i]})
			i++
			j++
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			ops = append(ops, op{'-', la[i]})
			i++
		default:
			ops = append(ops, op{'+', lb[j]})
			j++
		}
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", nameA, nameB)
	near := func(k int) bool {
		for d := -2; d <= 2; d++ {
			if k+d >= 0 && k+d < len(ops) && ops[k+d].kind != ' ' {
				return true
			}
		}
		return false
	}
	gap := false
	for k, o := range ops {
		if o.kind == ' ' && !near(k) {
			gap = true
			continue
		}
		if gap {
			out.WriteString("...\n")
			gap = false
		}
		out.WriteByte(o.kind)
		out.WriteString(o.text)
		out.WriteByte('\n')
	}
	return out.String()
}

func lines(b []byte) []string {
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
