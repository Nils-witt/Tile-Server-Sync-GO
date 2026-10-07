package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-sql-driver/mysql"
)

const defaultDatabasePort = 3306

// DSN assembles d's connection components into a Go MySQL driver DSN, e.g.
// "user:pass@tcp(127.0.0.1:3306)/dbname?parseTime=true&tls=true".
// parseTime=true is always set, since DATETIME columns must scan into
// time.Time (see internal/store). Params is appended verbatim; it's
// checked by validateConnection, not here.
func (d *Database) DSN() string {
	port := d.Port
	if port == 0 {
		port = defaultDatabasePort
	}

	cfg := mysql.NewConfig()
	cfg.User = d.User
	cfg.Passwd = d.Password
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(d.Host, strconv.Itoa(port))
	cfg.DBName = d.Name
	cfg.ParseTime = true

	dsn := cfg.FormatDSN()
	if d.Params != "" {
		dsn += "&" + d.Params
	}

	return dsn
}

// validateConnection checks d's connection components: host, user and
// database name are required, the port must be in range, and the assembled
// DSN (including Params) must parse.
func (d *Database) validateConnection() error {
	switch {
	case d.Host == "":
		return errors.New("database.host is required")
	case d.User == "":
		return errors.New("database.user is required")
	case d.Name == "":
		return errors.New("database.name is required")
	case d.Port < 0 || d.Port > 65535:
		return fmt.Errorf("database.port %d is out of range", d.Port)
	}

	if _, err := url.ParseQuery(d.Params); err != nil {
		return fmt.Errorf("database.params: %w", err)
	}

	if _, err := mysql.ParseDSN(d.DSN()); err != nil {
		return fmt.Errorf("database connection settings: %w", err)
	}

	return nil
}

// DatabaseFromDSN splits a Go MySQL driver DSN into d's connection
// components, for migrating a config stored before they were split up.
// Only TCP DSNs are supported; driver parameters other than parseTime are
// carried over into Params.
func DatabaseFromDSN(dsn string) (Database, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return Database{}, fmt.Errorf("parse dsn: %w", err)
	}

	if cfg.Net != "tcp" {
		return Database{}, fmt.Errorf("parse dsn: unsupported network %q", cfg.Net)
	}

	host, portStr, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return Database{}, fmt.Errorf("parse dsn address: %w", err)
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return Database{}, fmt.Errorf("parse dsn port: %w", err)
	}

	d := Database{Host: host, Port: port, User: cfg.User, Password: cfg.Passwd, Name: cfg.DBName}

	// Re-serialize without the fields held separately above, then keep
	// whatever query string remains as Params.
	cfg.User, cfg.Passwd, cfg.Addr, cfg.DBName, cfg.ParseTime = "", "", "", "", false

	_, d.Params, _ = strings.Cut(cfg.FormatDSN(), "?")

	return d, nil
}
