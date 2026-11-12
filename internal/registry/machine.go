package registry

import (
	"fmt"
	"regexp"
	"time"
)

// Machine is one fleet record, stored at machines/<name>.toml on every machine.
type Machine struct {
	Name          string   `toml:"name" json:"name"`
	OS            string   `toml:"os" json:"os"`
	Arch          string   `toml:"arch" json:"arch"`
	TailscaleIP   string   `toml:"tailscale_ip" json:"tailscale_ip"`
	DNSName       string   `toml:"dns_name" json:"dns_name"`
	SSHUser       string   `toml:"ssh_user" json:"ssh_user"`
	SSHPort       int      `toml:"ssh_port" json:"ssh_port"`
	YonderPath    string   `toml:"yonder_path" json:"yonder_path"`
	SSHHostKey    string   `toml:"ssh_host_key" json:"ssh_host_key"`
	SSHClientKeys []string `toml:"ssh_client_keys" json:"ssh_client_keys"`
	// the keys this machine itself offers when it sshes to the fleet, as it
	// reported them: ssh's default identities plus keys set with `yonder ssh-key`
	// there. mesh grants only these; ssh_client_keys stays the list to strip
	MeshKeys []string `toml:"mesh_keys,omitempty" json:"mesh_keys"`
	Role     string   `toml:"role,omitempty" json:"role"`
	Notes    string   `toml:"notes,omitempty" json:"notes"`
	Pending  bool     `toml:"pending,omitempty" json:"pending"`
	// the 1password service account whose token this machine holds, set by
	// `yonder secret setup` provisioning. a name, never a secret
	SecretsAccount string `toml:"secrets_account,omitempty" json:"secrets_account,omitempty"`
	// that account may also write the agent vault
	SecretsWrite bool      `toml:"secrets_write,omitempty" json:"secrets_write,omitempty"`
	UpdatedAt    time.Time `toml:"updated_at" json:"updated_at"`
	DiscoveredAt time.Time `toml:"discovered_at" json:"discovered_at"`
	AddedAt      time.Time `toml:"added_at,omitempty" json:"added_at"` // last `yonder add`; only this beats a tombstone
	Facts        Facts     `toml:"facts" json:"facts"`
}

// Facts is written by discovery and owned by the machine itself.
// step 2 fills these in; Extra holds anything without a typed field yet.
type Facts struct {
	Hostname    string         `toml:"hostname,omitempty" json:"hostname"`
	OS          string         `toml:"os,omitempty" json:"os"`
	Arch        string         `toml:"arch,omitempty" json:"arch"`
	OSVersion   string         `toml:"os_version,omitempty" json:"os_version"`
	Laptop      bool           `toml:"laptop,omitempty" json:"laptop"`
	CPU         string         `toml:"cpu,omitempty" json:"cpu"`
	CPUCores    int            `toml:"cpu_cores,omitempty" json:"cpu_cores"`
	MemoryGB    float64        `toml:"memory_gb,omitempty" json:"memory_gb"`
	DiskTotalGB float64        `toml:"disk_total_gb,omitempty" json:"disk_total_gb"`
	DiskFreeGB  float64        `toml:"disk_free_gb,omitempty" json:"disk_free_gb"`
	GPUs        []GPU          `toml:"gpus,omitempty" json:"gpus"`
	Models      []Model        `toml:"models,omitempty" json:"models"`
	Harnesses   []Harness      `toml:"harnesses,omitempty" json:"harnesses"`
	Extra       map[string]any `toml:"extra,omitempty" json:"extra,omitempty"`
}

type GPU struct {
	Name         string  `toml:"name" json:"name"`
	Vendor       string  `toml:"vendor,omitempty" json:"vendor"`
	MemoryGB     float64 `toml:"memory_gb,omitempty" json:"memory_gb"`
	MemoryUsedGB float64 `toml:"memory_used_gb,omitempty" json:"memory_used_gb"` // at discovery time
	UtilPercent  float64 `toml:"util_percent,omitempty" json:"util_percent"`     // at discovery time
	Unified      bool    `toml:"unified,omitempty" json:"unified"`               // shares system memory (apple silicon)
}

type Model struct {
	Name    string  `toml:"name" json:"name"`
	Runtime string  `toml:"runtime,omitempty" json:"runtime"` // ollama, lmstudio, llamacpp, vllm, mlx, ...
	SizeGB  float64 `toml:"size_gb,omitempty" json:"size_gb"`
	Source  string  `toml:"source,omitempty" json:"source"`   // how it was found: api | cli
	Serving bool    `toml:"serving,omitempty" json:"serving"` // a server answered at discovery time
	Port    int     `toml:"port,omitempty" json:"port"`       // local port when serving
}

type Harness struct {
	Name    string `toml:"name" json:"name"` // claude-code, codex, pi, hermes, opencode, gemini
	Version string `toml:"version,omitempty" json:"version"`
	Path    string `toml:"path,omitempty" json:"path"`
}

// Normalize replaces nil slices with empty ones so json output is stable.
func (f Facts) Normalize() Facts {
	if f.GPUs == nil {
		f.GPUs = []GPU{}
	}
	if f.Models == nil {
		f.Models = []Model{}
	}
	if f.Harnesses == nil {
		f.Harnesses = []Harness{}
	}
	return f
}

// Tombstone marks a removed machine, stored at removed/<name>.toml.
type Tombstone struct {
	Name      string    `toml:"name" json:"name"`
	RemovedAt time.Time `toml:"removed_at" json:"removed_at"`
	Reason    string    `toml:"reason,omitempty" json:"reason,omitempty"`
	// set by `remove --lost`: the keys to strip from authorized_keys everywhere
	Lost          bool     `toml:"lost,omitempty" json:"lost,omitempty"`
	SSHClientKeys []string `toml:"ssh_client_keys,omitempty" json:"ssh_client_keys,omitempty"`
	// `remove --lost --include-shared`: sync strips keys live records share too
	IncludeShared bool `toml:"include_shared,omitempty" json:"include_shared,omitempty"`
	// the record's secrets_account, so a rerun of remove --lost can name it
	SecretsAccount string `toml:"secrets_account,omitempty" json:"secrets_account,omitempty"`
	// keys of the record this machine held for that name. local only, never synced
	HeldKeys []string `toml:"held_keys,omitempty" json:"-"`
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidName reports whether name is a safe kebab-case machine name.
func ValidName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid machine name %q (want kebab-case: a-z, 0-9, -)", name)
	}
	return nil
}
