// Package discover gathers this machine's facts: hardware, local models and
// agent harnesses. every probe has its own short timeout and runs
// concurrently; a missing tool is simply absent, never an error.
package discover

import (
	"context"
	"os"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/zacharytgray/yonder/internal/registry"
)

type Facts = registry.Facts

// overall cap, so a stuck probe can never stall `add` or `refresh`.
const overall = 3 * time.Second

// Local gathers this machine's facts.
func Local() Facts {
	ctx, cancel := context.WithTimeout(context.Background(), overall)
	defer cancel()
	return collect(ctx)
}

func collect(ctx context.Context) Facts {
	host, _ := os.Hostname()
	f := Facts{
		Hostname: host,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		CPUCores: runtime.NumCPU(),
	}

	var (
		hw      hardware
		models  []registry.Model
		harness []registry.Harness
		wg      sync.WaitGroup
	)
	wg.Add(3)
	go func() { defer wg.Done(); hw = probeHardware(ctx) }()
	go func() { defer wg.Done(); models = probeModels(ctx) }()
	go func() { defer wg.Done(); harness = probeHarnesses(ctx) }()
	wg.Wait()

	f.OSVersion = hw.osVersion
	f.Laptop = hw.laptop
	f.CPU = hw.cpu
	f.MemoryGB = hw.memoryGB
	f.DiskTotalGB = hw.diskTotalGB
	f.DiskFreeGB = hw.diskFreeGB
	f.GPUs = hw.gpus
	f.Harnesses = harness

	sort.SliceStable(models, func(i, j int) bool {
		if models[i].Runtime != models[j].Runtime {
			return models[i].Runtime < models[j].Runtime
		}
		return models[i].Name < models[j].Name
	})
	f.Models = models
	return f.Normalize()
}

// gb rounds a byte count to GiB with one decimal.
func gb(bytes float64) float64 {
	return float64(int64(bytes/(1<<30)*10+0.5)) / 10
}
