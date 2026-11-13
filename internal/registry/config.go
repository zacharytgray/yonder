package registry

import (
	"errors"
	"os"

	"github.com/pelletier/go-toml/v2"
)

// Config is this machine's config.toml.
type Config struct {
	Self     string         `toml:"self"`
	Secrets  map[string]any `toml:"secrets,omitempty"`  // provider settings, step 3
	SSH      map[string]SSH `toml:"ssh,omitempty"`      // per-target ssh settings, this machine only
	Discover Discover       `toml:"discover,omitempty"` // hand-edited discovery settings, this machine only
	Sync     SyncSettings   `toml:"sync,omitempty"`     // hand-edited store sync settings, this machine only
}

// SyncSettings tunes store sync on this machine. yonder only reads it.
type SyncSettings struct {
	// which incoming changes wait for `yonder agents accept`: executables (default) | all | none
	Review string `toml:"review,omitempty"`
}

// Discover tunes discovery on this machine. yonder only reads it.
type Discover struct {
	ModelPorts []int `toml:"model_ports,omitempty"` // more local openai-compatible ports to probe
}

// SSH is how this machine reaches one target. it never leaves this machine.
type SSH struct {
	Identity string `toml:"identity,omitempty"` // private key path, ~ left unexpanded
}

func (s *Store) Config() (Config, error) {
	var c Config
	err := readTOML(s.configPath(), &c)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	return c, err
}

// SetSelf sets self = name and keeps every other key in config.toml.
func (s *Store) SetSelf(name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	raw := map[string]any{}
	if err := readTOML(s.configPath(), &raw); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw["self"] = name
	data, err := toml.Marshal(raw)
	if err != nil {
		return err
	}
	return writeFile(s.configPath(), data)
}

// SetSecrets replaces the [secrets] table and keeps every other key.
func (s *Store) SetSecrets(t map[string]any) error {
	raw := map[string]any{}
	if err := readTOML(s.configPath(), &raw); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(t) == 0 {
		delete(raw, "secrets")
	} else {
		raw["secrets"] = t
	}
	data, err := toml.Marshal(raw)
	if err != nil {
		return err
	}
	return writeFile(s.configPath(), data)
}

// Identity is the ssh key configured for target, as written (~ unexpanded).
func (s *Store) Identity(target string) string {
	c, err := s.Config()
	if err != nil {
		return ""
	}
	return c.SSH[target].Identity
}

// SetIdentity sets or (with "") clears the ssh key for target and keeps every other key.
func (s *Store) SetIdentity(target, path string) error {
	if err := ValidName(target); err != nil {
		return err
	}
	raw := map[string]any{}
	if err := readTOML(s.configPath(), &raw); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tbl, _ := raw["ssh"].(map[string]any)
	if tbl == nil {
		tbl = map[string]any{}
	}
	if path == "" {
		delete(tbl, target)
	} else {
		tbl[target] = map[string]any{"identity": path}
	}
	if len(tbl) == 0 {
		delete(raw, "ssh")
	} else {
		raw["ssh"] = tbl
	}
	data, err := toml.Marshal(raw)
	if err != nil {
		return err
	}
	return writeFile(s.configPath(), data)
}

// Forget clears this machine's copy of the registry, its layers and its name, keeping
// the rest of config.toml. a removed machine runs it to leave the fleet.
func (s *Store) Forget() error {
	for _, d := range []string{s.machinesDir(), s.removedDir(), s.layersPath()} {
		if err := os.RemoveAll(d); err != nil {
			return err
		}
	}
	raw := map[string]any{}
	if err := readTOML(s.configPath(), &raw); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	delete(raw, "self")
	data, err := toml.Marshal(raw)
	if err != nil {
		return err
	}
	return writeFile(s.configPath(), data)
}
