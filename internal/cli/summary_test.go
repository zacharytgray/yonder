package cli

import (
	"testing"

	"github.com/zacharytgray/yonder/internal/registry"
)

func TestSummary(t *testing.T) {
	cases := []struct {
		name string
		f    registry.Facts
		want string
	}{
		{"apple", registry.Facts{MemoryGB: 36, GPUs: []registry.GPU{{Name: "Apple M3 Pro", Unified: true, MemoryGB: 36}}}, "M3 Pro · 36 GB"},
		{"nvidia", registry.Facts{MemoryGB: 64, GPUs: []registry.GPU{{Name: "NVIDIA GeForce RTX 4090", MemoryGB: 24}}}, "RTX 4090 24 GB · 64 GB"},
		{"two gpus", registry.Facts{MemoryGB: 128, GPUs: []registry.GPU{{Name: "NVIDIA A100", MemoryGB: 40}, {Name: "NVIDIA A100", MemoryGB: 40}}}, "2× A100 40 GB · 128 GB"},
		{"cpu only", registry.Facts{MemoryGB: 32, CPU: "Intel(R) Core(TM) i7-8700B CPU @ 3.20GHz", GPUs: []registry.GPU{{Name: "Intel UHD Graphics 630", MemoryGB: 1.5}}}, "Intel Core i7-8700B · 32 GB"},
		{"amd cpu", registry.Facts{MemoryGB: 62.8, CPU: "AMD Ryzen 9 7950X 16-Core Processor"}, "AMD Ryzen 9 7950X · 62.8 GB"},
		{"cores", registry.Facts{CPUCores: 4}, "4 cores"},
		{"empty", registry.Facts{}, ""},
	}
	for _, c := range cases {
		if got := summary(c.f); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
