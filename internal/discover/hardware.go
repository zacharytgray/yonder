package discover

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zacharytgray/yonder/internal/registry"
)

type hardware struct {
	osVersion   string
	laptop      bool
	cpu         string
	memoryGB    float64
	diskTotalGB float64
	diskFreeGB  float64
	gpus        []registry.GPU
}

const probeTimeout = 1500 * time.Millisecond

func probeHardware(ctx context.Context) hardware {
	var hw hardware
	var wg sync.WaitGroup
	do := func(fn func()) {
		wg.Add(1)
		go func() { defer wg.Done(); fn() }()
	}
	do(func() { hw.diskTotalGB, hw.diskFreeGB = diskUsage("/") })
	switch runtime.GOOS {
	case "darwin":
		do(func() { hw.cpu = darwinCPU(ctx) })
		do(func() { hw.memoryGB = darwinMemory(ctx) })
		do(func() { hw.osVersion = darwinOS(ctx) })
		do(func() { hw.laptop = darwinLaptop(ctx) })
	case "linux":
		do(func() { hw.cpu = linuxCPU(ctx) })
		do(func() { hw.memoryGB = linuxMemory() })
		do(func() { hw.osVersion = linuxOS() })
		do(func() { hw.laptop = linuxLaptop() })
	}
	var nv, amd, mac []registry.GPU
	switch runtime.GOOS {
	case "linux":
		do(func() { nv = nvidiaGPUs(ctx) })
		do(func() { amd = amdGPUs(ctx) })
	case "darwin":
		do(func() { mac = darwinGPUs(ctx) })
	}
	wg.Wait()
	hw.gpus = append(append(nv, amd...), mac...)
	// apple silicon: the chip is the gpu, and it shares system memory
	if len(hw.gpus) == 0 && strings.HasPrefix(hw.cpu, "Apple ") {
		hw.gpus = []registry.GPU{{Name: hw.cpu, Vendor: "apple", MemoryGB: hw.memoryGB, Unified: true}}
	}
	for i := range hw.gpus {
		if hw.gpus[i].Unified && hw.gpus[i].MemoryGB == 0 {
			hw.gpus[i].MemoryGB = hw.memoryGB
		}
	}
	return hw
}

func diskUsage(path string) (total, free float64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	bs := float64(st.Bsize)
	return gb(float64(st.Blocks) * bs), gb(float64(st.Bavail) * bs)
}

// darwin

func darwinCPU(ctx context.Context) string {
	bin := find("sysctl")
	if bin == "" {
		return ""
	}
	out, _ := run(ctx, probeTimeout, bin, "-n", "machdep.cpu.brand_string")
	return strings.TrimSpace(out)
}

func darwinMemory(ctx context.Context) float64 {
	bin := find("sysctl")
	if bin == "" {
		return 0
	}
	out, ok := run(ctx, probeTimeout, bin, "-n", "hw.memsize")
	if !ok {
		return 0
	}
	n, _ := strconv.ParseFloat(strings.TrimSpace(out), 64)
	return gb(n)
}

func darwinOS(ctx context.Context) string {
	bin := find("sw_vers")
	if bin == "" {
		return ""
	}
	out, _ := run(ctx, probeTimeout, bin)
	return parseSwVers(out)
}

func darwinLaptop(ctx context.Context) bool {
	bin := find("pmset")
	if bin == "" {
		return false
	}
	out, _ := run(ctx, probeTimeout, bin, "-g", "batt")
	return parsePmsetBattery(out)
}

func darwinGPUs(ctx context.Context) []registry.GPU {
	// apple silicon is handled from the cpu brand; only intel macs need this
	if runtime.GOARCH == "arm64" {
		return nil
	}
	bin := find("system_profiler")
	if bin == "" {
		return nil
	}
	out, _ := run(ctx, 2*time.Second, bin, "SPDisplaysDataType")
	return parseSPDisplays(out)
}

// parseSwVers turns `sw_vers` output into "macOS 15.1".
func parseSwVers(out string) string {
	var name, ver string
	for _, l := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "ProductName":
			name = strings.TrimSpace(v)
		case "ProductVersion":
			ver = strings.TrimSpace(v)
		}
	}
	if name == "Mac OS X" {
		name = "macOS"
	}
	return strings.TrimSpace(name + " " + ver)
}

// parsePmsetBattery reports whether `pmset -g batt` lists an internal battery.
func parsePmsetBattery(out string) bool {
	return strings.Contains(out, "InternalBattery")
}

var vramRe = regexp.MustCompile(`(?i)([\d.]+)\s*(MB|GB)`)

// parseSPDisplays reads `system_profiler SPDisplaysDataType` (intel macs).
func parseSPDisplays(out string) []registry.GPU {
	var gpus []registry.GPU
	var cur *registry.GPU
	flush := func() {
		if cur != nil && cur.Name != "" {
			gpus = append(gpus, *cur)
		}
		cur = nil
	}
	for _, l := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch {
		case k == "Chipset Model":
			flush()
			cur = &registry.GPU{Name: v, Vendor: vendorOf(v)}
		case cur == nil:
		case strings.HasPrefix(k, "VRAM"):
			if m := vramRe.FindStringSubmatch(v); m != nil {
				n, _ := strconv.ParseFloat(m[1], 64)
				if strings.EqualFold(m[2], "MB") {
					n /= 1024
				}
				cur.MemoryGB = float64(int64(n*10+0.5)) / 10
			}
		case k == "Vendor":
			if vd := vendorOf(v); vd != "" {
				cur.Vendor = vd
			}
		}
	}
	flush()
	return gpus
}

func vendorOf(s string) string {
	s = strings.ToLower(s)
	switch {
	case strings.Contains(s, "nvidia"), strings.Contains(s, "geforce"), strings.Contains(s, "quadro"):
		return "nvidia"
	case strings.Contains(s, "amd"), strings.Contains(s, "ati"), strings.Contains(s, "radeon"):
		return "amd"
	case strings.Contains(s, "intel"):
		return "intel"
	case strings.Contains(s, "apple"):
		return "apple"
	}
	return ""
}

// linux

func linuxCPU(ctx context.Context) string {
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		if n := parseCPUInfo(string(b)); n != "" {
			return n
		}
	}
	if bin := find("lscpu"); bin != "" {
		out, _ := run(ctx, probeTimeout, bin)
		return parseLscpu(out)
	}
	return ""
}

func parseCPUInfo(s string) string {
	for _, l := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(l, ":")
		if ok && strings.EqualFold(strings.TrimSpace(k), "model name") {
			return strings.Join(strings.Fields(v), " ")
		}
	}
	return ""
}

func parseLscpu(s string) string {
	for _, l := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(l, ":")
		if ok && strings.TrimSpace(k) == "Model name" {
			return strings.Join(strings.Fields(v), " ")
		}
	}
	return ""
}

func linuxMemory() float64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	return parseMemInfo(string(b))
}

func parseMemInfo(s string) float64 {
	for _, l := range strings.Split(s, "\n") {
		if rest, ok := strings.CutPrefix(l, "MemTotal:"); ok {
			f := strings.Fields(rest)
			if len(f) > 0 {
				kb, _ := strconv.ParseFloat(f[0], 64)
				return gb(kb * 1024)
			}
		}
	}
	return 0
}

func linuxOS() string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		b, _ = os.ReadFile("/usr/lib/os-release")
	}
	return parseOSRelease(string(b))
}

// parseOSRelease prefers PRETTY_NAME, else NAME and VERSION_ID.
func parseOSRelease(s string) string {
	kv := map[string]string{}
	for _, l := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		v = strings.TrimSpace(v)
		if u, err := strconv.Unquote(v); err == nil {
			v = u
		} else {
			v = strings.Trim(v, `'"`)
		}
		kv[k] = v
	}
	if kv["PRETTY_NAME"] != "" {
		return kv["PRETTY_NAME"]
	}
	return strings.TrimSpace(kv["NAME"] + " " + kv["VERSION_ID"])
}

func linuxLaptop() bool {
	m, _ := filepath.Glob("/sys/class/power_supply/BAT*")
	return len(m) > 0
}

// nvidiaGPUs runs nvidia-smi; absent or failing means no nvidia gpu.
func nvidiaGPUs(ctx context.Context) []registry.GPU {
	bin := find("nvidia-smi", "/usr/lib/wsl/lib")
	if bin == "" {
		return nil
	}
	out, ok := run(ctx, 2*time.Second, bin,
		"--query-gpu=name,memory.total,memory.used,utilization.gpu", "--format=csv,noheader,nounits")
	if !ok {
		return nil
	}
	return parseNvidiaSMI(out)
}

// parseNvidiaSMI reads name,memory.total,memory.used,utilization.gpu csv
// (memory in MiB, no units).
func parseNvidiaSMI(out string) []registry.GPU {
	var gpus []registry.GPU
	num := func(s string) float64 {
		n, _ := strconv.ParseFloat(strings.TrimSpace(s), 64) // "[N/A]" gives 0
		return n
	}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(l, ",")
		if len(f) < 2 || strings.TrimSpace(f[0]) == "" {
			continue
		}
		g := registry.GPU{
			Name:     strings.TrimSpace(f[0]),
			Vendor:   "nvidia",
			MemoryGB: gb(num(f[1]) * (1 << 20)),
		}
		if len(f) > 2 {
			g.MemoryUsedGB = gb(num(f[2]) * (1 << 20))
		}
		if len(f) > 3 {
			g.UtilPercent = num(f[3])
		}
		gpus = append(gpus, g)
	}
	return gpus
}

func amdGPUs(ctx context.Context) []registry.GPU {
	bin := find("rocm-smi", "/opt/rocm/bin")
	if bin == "" {
		return nil
	}
	out, ok := run(ctx, 2*time.Second, bin, "--showproductname", "--showmeminfo", "vram", "--json")
	if !ok {
		return nil
	}
	return parseRocmSMI(out)
}

// parseRocmSMI reads `rocm-smi --showproductname --showmeminfo vram --json`.
func parseRocmSMI(out string) []registry.GPU {
	// rocm-smi sometimes prints warnings before the json
	if i := strings.Index(out, "{"); i >= 0 {
		out = out[i:]
	}
	var cards map[string]map[string]any
	if json.Unmarshal([]byte(out), &cards) != nil {
		return nil
	}
	var gpus []registry.GPU
	for i := 0; i < len(cards); i++ {
		c, ok := cards["card"+strconv.Itoa(i)]
		if !ok {
			continue
		}
		g := registry.GPU{Vendor: "amd"}
		for k, v := range c {
			s, _ := v.(string)
			lk := strings.ToLower(k)
			switch {
			case lk == "card series" || lk == "card model" && g.Name == "":
				g.Name = s
			case strings.HasPrefix(lk, "vram total memory"):
				n, _ := strconv.ParseFloat(s, 64)
				g.MemoryGB = gb(n)
			case strings.HasPrefix(lk, "vram total used memory"):
				n, _ := strconv.ParseFloat(s, 64)
				g.MemoryUsedGB = gb(n)
			}
		}
		if g.Name == "" {
			g.Name = "AMD GPU"
		}
		gpus = append(gpus, g)
	}
	return gpus
}
