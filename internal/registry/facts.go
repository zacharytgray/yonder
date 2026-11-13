package registry

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	parenRe = regexp.MustCompile(`\((R|TM|tm|r)\)`)
	cpuTail = regexp.MustCompile(`\s+(CPU|Processor)\b.*$|\s+@.*$|\s+\d+-Core.*$`)
)

// ShortCPU trims marketing noise: "Intel(R) Core(TM) i7-8700B CPU @ 3.20GHz" -> "Intel Core i7-8700B".
func ShortCPU(s string) string {
	s = parenRe.ReplaceAllString(s, "")
	s = cpuTail.ReplaceAllString(s, "")
	s = strings.TrimPrefix(s, "Apple ")
	return strings.Join(strings.Fields(s), " ")
}

// ShortGPU drops vendor prefixes: "NVIDIA GeForce RTX 4090" -> "RTX 4090".
func ShortGPU(s string) string {
	for _, p := range []string{"NVIDIA GeForce ", "NVIDIA ", "AMD Radeon ", "AMD ", "Apple "} {
		s = strings.TrimPrefix(s, p)
	}
	return s
}

func GBText(n float64) string {
	if n == float64(int64(n)) {
		return fmt.Sprintf("%d GB", int64(n))
	}
	return fmt.Sprintf("%.1f GB", n)
}
