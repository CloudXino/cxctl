package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
)

// config is the saved logins, kept in a file only the user can read.
type config struct {
	Current  string                   `json:"current"`
	Contexts map[string]configContext `json:"contexts"`
}

type configContext struct {
	Token  string `json:"token"`
	APIURL string `json:"api_url,omitempty"`
	// shown in `auth list`, filled at login
	Team      string `json:"team,omitempty"`
	Role      string `json:"role,omitempty"`
	KeyPrefix string `json:"key_prefix,omitempty"`
}

func defaultConfigPathHint() string {
	if p, err := defaultConfigPath(); err == nil {
		return p
	}
	return "$XDG_CONFIG_HOME/cxctl/config.json"
}

func defaultConfigPath() (string, error) {
	if p := os.Getenv("CXCTL_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "cxctl", "config.json"), nil
}

func (a *app) configFile() string {
	if a.configPath != "" {
		return a.configPath
	}
	p, err := defaultConfigPath()
	if err != nil {
		return "config.json"
	}
	return p
}

func loadConfig(path string) (*config, error) {
	cfg := &config{Contexts: map[string]configContext{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if cfg.Contexts == nil {
		cfg.Contexts = map[string]configContext{}
	}
	return cfg, nil
}

// save writes the file with mode 0600 (it holds API keys), replacing it in one step.
func (c *config) save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (c *config) names() []string {
	out := make([]string, 0, len(c.Contexts))
	for n := range c.Contexts {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func httpStatusText(code int) string {
	if t := http.StatusText(code); t != "" {
		return t
	}
	return fmt.Sprintf("HTTP %d", code)
}
