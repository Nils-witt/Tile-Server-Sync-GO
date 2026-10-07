package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// defaultConfigDBName is the filename LoadBootstrap defaults ConfigDB to
// when the bootstrap file leaves it unset.
const defaultConfigDBName = "config.db"

// Bootstrap is the minimal file/CLI-driven config: just enough to find the
// SQLite database holding everything else (API credentials, database
// target, maps — each with its own optional sync interval), to configure
// the status/config web server (which always runs), and to configure the
// OIDC single sign-on it requires. WebServer lives here rather than in that database
// because changing it already requires a process restart (the HTTP server
// can't restart itself mid-request), so there's nothing to gain by making it
// reloadable; SSO lives here so login settings are managed alongside the
// server they protect, outside the web UI those logins grant access to.
type Bootstrap struct {
	WebServer WebServer `yaml:"webServer" json:"webServer"`
	// ConfigDB is the path to the SQLite database holding the rest of the
	// configuration. A relative path is resolved against the directory
	// containing the bootstrap file itself (mirroring how main.go's
	// openLogFile places the log file next to it), not the process's
	// working directory. Defaults to "config.db" if left empty.
	ConfigDB string `yaml:"configDb" json:"configDb"`
	// SSO is the OpenID Connect login configuration, under the "oidc" key
	// (see SSO) — the web server's only login method, so it's required.
	SSO SSO `yaml:"oidc" json:"oidc"`
}

// LoadBootstrap reads and parses the minimal bootstrap YAML file at path,
// applying the same WebServer.Address defaulting Config.Validate does,
// defaulting and validating SSO, and resolving ConfigDB (defaulted to
// "config.db") relative to path's directory.
func LoadBootstrap(path string) (*Bootstrap, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is a trusted, user-supplied CLI flag
	if err != nil {
		return nil, fmt.Errorf("read bootstrap config %q: %w", path, err)
	}

	var b Bootstrap
	if err := yaml.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("parse bootstrap config %q: %w", path, err)
	}

	b.WebServer.applyDefault()
	b.SSO.applyDefaults()

	if err := b.SSO.validate(); err != nil {
		return nil, fmt.Errorf("bootstrap config %q: %w", path, err)
	}

	if b.ConfigDB == "" {
		b.ConfigDB = defaultConfigDBName
	}

	if !filepath.IsAbs(b.ConfigDB) {
		b.ConfigDB = filepath.Join(filepath.Dir(path), b.ConfigDB)
	}

	return &b, nil
}
