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
// target, maps, interval) and to configure the optional status/config web
// server. WebServer lives here rather than in that database because
// changing it already requires a process restart (the HTTP server can't
// restart itself mid-request), so there's nothing to gain by making it
// reloadable.
type Bootstrap struct {
	WebServer WebServer `yaml:"webServer" json:"webServer"`
	// ConfigDB is the path to the SQLite database holding the rest of the
	// configuration. A relative path is resolved against the directory
	// containing the bootstrap file itself (mirroring how main.go's
	// openLogFile places the log file next to it), not the process's
	// working directory. Defaults to "config.db" if left empty.
	ConfigDB string `yaml:"configDb" json:"configDb"`
}

// LoadBootstrap reads and parses the minimal bootstrap YAML file at path,
// applying the same WebServer.Address defaulting Config.Validate does and
// resolving ConfigDB (defaulted to "config.db") relative to path's
// directory.
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

	if b.ConfigDB == "" {
		b.ConfigDB = defaultConfigDBName
	}

	if !filepath.IsAbs(b.ConfigDB) {
		b.ConfigDB = filepath.Join(filepath.Dir(path), b.ConfigDB)
	}

	return &b, nil
}
