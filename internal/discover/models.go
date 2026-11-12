package discover

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zacharytgray/yonder/internal/registry"
)

const (
	httpTimeout = time.Second
	maxModels   = 50 // per runtime, so an odd server can't flood the record
)

var client = &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}

// defaultPorts are probed on every machine; model_ports in config.toml adds more.
var defaultPorts = []int{11434, 1234, 8080, 8000}

// maxExtraPorts caps model_ports, so a typo'd range can't stall discovery.
const maxExtraPorts = 8

// extraPorts keeps the valid model_ports that aren't probed already.
func extraPorts(ports []int) []int {
	var out []int
	for _, p := range ports {
		if p < 1 || p > 65535 || slices.Contains(defaultPorts, p) || slices.Contains(out, p) {
			continue
		}
		out = append(out, p)
		if len(out) == maxExtraPorts {
			break
		}
	}
	return out
}

func probeModels(ctx context.Context, extra []int) []registry.Model {
	var (
		mu  sync.Mutex
		all []registry.Model
		wg  sync.WaitGroup
	)
	add := func(ms []registry.Model) {
		mu.Lock()
		all = append(all, ms...)
		mu.Unlock()
	}
	do := func(fn func() []registry.Model) {
		wg.Add(1)
		go func() { defer wg.Done(); add(fn()) }()
	}
	do(func() []registry.Model { return probeOllama(ctx, "http://127.0.0.1:11434") })
	do(func() []registry.Model { return probeLMStudio(ctx, "http://127.0.0.1:1234") })
	do(func() []registry.Model { return probeOpenAI(ctx, "http://127.0.0.1:8080") })
	do(func() []registry.Model { return probeOpenAI(ctx, "http://127.0.0.1:8000") })
	for _, p := range extraPorts(extra) {
		do(func() []registry.Model { return probeOpenAI(ctx, "http://127.0.0.1:"+strconv.Itoa(p)) })
	}
	wg.Wait()
	return all
}

// getJSON fetches url with a short timeout. ok is false on any failure.
func getJSON(ctx context.Context, url string, v any) bool {
	ctx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(v) == nil
}

func portOf(base string) int {
	i := strings.LastIndex(base, ":")
	n, _ := strconv.Atoi(base[i+1:])
	return n
}

func capModels(ms []registry.Model) []registry.Model {
	if len(ms) > maxModels {
		return ms[:maxModels]
	}
	return ms
}

// ollama

func probeOllama(ctx context.Context, base string) []registry.Model {
	var tags struct {
		Models []struct {
			Name string  `json:"name"`
			Size float64 `json:"size"`
		} `json:"models"`
	}
	if getJSON(ctx, base+"/api/tags", &tags) {
		var ms []registry.Model
		for _, m := range tags.Models {
			ms = append(ms, registry.Model{Name: m.Name, Runtime: "ollama", SizeGB: gb(m.Size),
				Source: "api", Serving: true, Port: portOf(base)})
		}
		return capModels(ms)
	}
	bin := find("ollama")
	if bin == "" {
		return nil
	}
	out, ok := run(ctx, 2*time.Second, bin, "list")
	if !ok {
		return nil
	}
	return capModels(parseOllamaList(out))
}

var gap = regexp.MustCompile(`\s{2,}`)

// parseOllamaList reads `ollama list` (NAME  ID  SIZE  MODIFIED).
func parseOllamaList(out string) []registry.Model {
	var ms []registry.Model
	for i, l := range strings.Split(out, "\n") {
		if i == 0 || strings.TrimSpace(l) == "" {
			continue // header
		}
		f := gap.Split(strings.TrimSpace(l), -1)
		if len(f) < 3 {
			continue
		}
		ms = append(ms, registry.Model{Name: f[0], Runtime: "ollama", SizeGB: parseSize(f[2]), Source: "cli"})
	}
	return ms
}

var sizeRe = regexp.MustCompile(`(?i)^([\d.]+)\s*(KB|MB|GB|TB|B)$`)

// parseSize turns "4.7 GB" into GiB-ish gigabytes (decimal units, as ollama prints).
func parseSize(s string) float64 {
	m := sizeRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	n, _ := strconv.ParseFloat(m[1], 64)
	mult := map[string]float64{"B": 1, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12}[strings.ToUpper(m[2])]
	return gb(n * mult)
}

// lm studio

func probeLMStudio(ctx context.Context, base string) []registry.Model {
	var (
		served []registry.Model
		listed []registry.Model
		wg     sync.WaitGroup
	)
	wg.Add(2)
	go func() { defer wg.Done(); served = probeOpenAIModels(ctx, base, "lmstudio") }()
	go func() {
		defer wg.Done()
		if bin := find("lms"); bin != "" {
			if out, ok := run(ctx, 2*time.Second, bin, "ls", "--json"); ok {
				listed = parseLMSList(out)
			}
		}
	}()
	wg.Wait()
	// downloaded models give sizes; the server says which are loaded
	byName := map[string]int{}
	for i, m := range listed {
		byName[m.Name] = i
	}
	for _, s := range served {
		if i, ok := byName[s.Name]; ok {
			listed[i].Serving, listed[i].Port = true, s.Port
		} else {
			listed = append(listed, s)
		}
	}
	return capModels(listed)
}

// parseLMSList reads `lms ls --json`.
func parseLMSList(out string) []registry.Model {
	var items []struct {
		ModelKey  string  `json:"modelKey"`
		Path      string  `json:"path"`
		SizeBytes float64 `json:"sizeBytes"`
		Type      string  `json:"type"`
	}
	if json.Unmarshal([]byte(out), &items) != nil {
		return nil
	}
	var ms []registry.Model
	for _, it := range items {
		if it.Type == "embedding" {
			continue
		}
		name := it.ModelKey
		if name == "" {
			name = it.Path
		}
		if name == "" {
			continue
		}
		ms = append(ms, registry.Model{Name: name, Runtime: "lmstudio", SizeGB: gb(it.SizeBytes), Source: "cli"})
	}
	return ms
}

// openai-compatible servers (llama.cpp, vllm, mlx)

func probeOpenAI(ctx context.Context, base string) []registry.Model {
	return capModels(probeOpenAIModels(ctx, base, ""))
}

func probeOpenAIModels(ctx context.Context, base, runtime string) []registry.Model {
	var raw json.RawMessage
	if !getJSON(ctx, base+"/v1/models", &raw) {
		return nil
	}
	return parseOpenAIModels(raw, runtime, portOf(base))
}

// parseOpenAIModels reads a /v1/models reply. runtime is forced when known,
// else it is guessed from owned_by.
func parseOpenAIModels(body []byte, runtime string, port int) []registry.Model {
	var reply struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
			Meta    struct {
				Size float64 `json:"size"`
			} `json:"meta"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &reply) != nil {
		return nil
	}
	var ms []registry.Model
	for _, d := range reply.Data {
		if d.ID == "" {
			continue
		}
		rt := runtime
		if rt == "" {
			rt = guessRuntime(d.OwnedBy, d.ID)
		}
		ms = append(ms, registry.Model{Name: d.ID, Runtime: rt, SizeGB: gb(d.Meta.Size),
			Source: "api", Serving: true, Port: port})
	}
	return ms
}

func guessRuntime(ownedBy, id string) string {
	s := strings.ToLower(ownedBy)
	switch {
	case strings.Contains(s, "llama-swap"), strings.Contains(s, "llamaswap"):
		return "llama-swap"
	case strings.Contains(s, "vllm"):
		return "vllm"
	case strings.Contains(s, "llamacpp"), strings.Contains(s, "llama.cpp"):
		return "llamacpp"
	case strings.Contains(s, "mlx"), strings.Contains(strings.ToLower(id), "mlx"):
		return "mlx"
	}
	return "openai-api"
}
