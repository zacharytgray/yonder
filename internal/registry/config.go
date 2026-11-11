package registry

import (
	"errors"
	"os"

	"github.com/pelletier/go-toml/v2"
)

// Config is this machine's config.toml.
type Config struct {
	Self    string         `toml:"self"`
	Secrets map[string]any `toml:"secrets,omitempty"` // provider settings, step 3
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

// Forget clears this machine's copy of the registry and its name, keeping
// the rest of config.toml. a removed machine runs it to leave the fleet.
func (s *Store) Forget() error {
	for _, d := range []string{s.machinesDir(), s.removedDir()} {
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
