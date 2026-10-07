package config

import (
	"crypto/tls"
	"crypto/x509"
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
// time.Time (see internal/store). TLS/TLSSkipVerify become the driver's
// tls=true (verify the certificate against the system roots and Host) or
// tls=skip-verify. Params is appended verbatim; it's checked by
// ValidateConnection, not here. A DSN can't carry TLSCACert — connect via
// DriverConfig, which can.
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

	if d.TLS {
		cfg.TLSConfig = "true"
		if d.TLSSkipVerify {
			cfg.TLSConfig = "skip-verify"
		}
	}

	dsn := cfg.FormatDSN()
	if d.Params != "" {
		dsn += "&" + d.Params
	}

	return dsn
}

// ValidateConnection checks d's connection components: host, user and
// database name are required, the port must be in range, and the assembled
// DSN (including Params) must parse. Params may not set "tls" itself —
// that's what TLS/TLSSkipVerify are for.
func (d *Database) ValidateConnection() error {
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

	params, err := url.ParseQuery(d.Params)
	if err != nil {
		return fmt.Errorf("database.params: %w", err)
	}

	if params.Has(tlsParam) {
		return errors.New("database.params may not set tls, use the TLS options instead")
	}

	if _, err := d.DriverConfig(); err != nil {
		return fmt.Errorf("database connection settings: %w", err)
	}

	return nil
}

// DriverConfig returns d as a Go MySQL driver config, for
// mysql.NewConnector: DSN(), parsed, plus — if TLS verification uses a
// custom CA (TLSCACert) — a TLS config trusting only those certificates.
func (d *Database) DriverConfig() (*mysql.Config, error) {
	cfg, err := mysql.ParseDSN(d.DSN())
	if err != nil {
		return nil, err
	}

	if d.TLS && !d.TLSSkipVerify && strings.TrimSpace(d.TLSCACert) != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(d.TLSCACert)) {
			return nil, errors.New("database.tlsCaCert contains no valid PEM certificate")
		}

		cfg.TLS = &tls.Config{RootCAs: pool, ServerName: d.Host, MinVersion: tls.VersionTLS12}
	}

	return cfg, nil
}

// DatabaseFromDSN splits a Go MySQL driver DSN into d's connection
// components, for migrating a config stored before they were split up.
// Only TCP DSNs are supported; driver parameters other than parseTime are
// carried over into Params, except tls (see LiftTLSParam).
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
	d.LiftTLSParam()

	return d, nil
}

const tlsParam = "tls"

// LiftTLSParam moves a "tls" driver parameter out of d.Params into
// d.TLS/d.TLSSkipVerify, for a config saved before TLS had its own options.
// Only values those options can express are moved (true/1, skip-verify,
// false/0); any other value (e.g. "preferred") is left in Params, where
// ValidateConnection rejects it so the user re-enters it. Params is left
// untouched unless the parameter is moved.
func (d *Database) LiftTLSParam() {
	params, err := url.ParseQuery(d.Params)
	if err != nil || !params.Has(tlsParam) {
		return
	}

	switch strings.ToLower(params.Get(tlsParam)) {
	case "true", "1":
		d.TLS, d.TLSSkipVerify = true, false
	case "skip-verify":
		d.TLS, d.TLSSkipVerify = true, true
	case "false", "0":
		d.TLS, d.TLSSkipVerify = false, false
	default:
		return
	}

	params.Del(tlsParam)
	d.Params = params.Encode()
}
