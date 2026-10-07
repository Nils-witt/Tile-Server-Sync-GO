package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

const timeoutParam = "timeout=10s"

func TestDatabaseDSNRoundtrip(t *testing.T) {
	t.Parallel()

	d := Database{
		Host: "db.example", User: "u", Password: "p@ss:w/rd", Name: "geo",
		Params: timeoutParam, TLS: true,
	}

	if err := d.ValidateConnection(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	want := "u:p@ss:w/rd@tcp(db.example:3306)/geo?parseTime=true&tls=true&timeout=10s"
	if got := d.DSN(); got != want {
		t.Errorf("DSN() = %q, want %q", got, want)
	}

	parsed, err := DatabaseFromDSN(d.DSN())
	if err != nil {
		t.Fatalf("DatabaseFromDSN: %v", err)
	}

	if parsed.Host != d.Host || parsed.Port != defaultDatabasePort || parsed.User != d.User ||
		parsed.Password != d.Password || parsed.Name != d.Name || parsed.Params != d.Params ||
		parsed.TLS != d.TLS || parsed.TLSSkipVerify != d.TLSSkipVerify {
		t.Errorf("roundtrip = %+v, want %+v", parsed, d)
	}
}

func TestDatabaseValidateConnection(t *testing.T) {
	t.Parallel()

	valid := Database{Host: "h", User: "u", Name: "n"}

	for name, mutate := range map[string]func(*Database){
		"missing host":  func(d *Database) { d.Host = "" },
		"missing user":  func(d *Database) { d.User = "" },
		"missing name":  func(d *Database) { d.Name = "" },
		"bad port":      func(d *Database) { d.Port = 70000 },
		"bad params":    func(d *Database) { d.Params = "timeout=notaduration" },
		"tls in params": func(d *Database) { d.Params = "tls=true" },
	} {
		d := valid
		mutate(&d)

		if err := d.ValidateConnection(); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestDatabaseFromDSNRejectsUnixSocket(t *testing.T) {
	t.Parallel()

	if _, err := DatabaseFromDSN("u:p@unix(/tmp/mysql.sock)/db"); err == nil {
		t.Error("expected error for unix socket DSN")
	}
}

func TestDatabaseDSNTLS(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		tls, skipVerify bool
		want            string
	}{
		{false, false, "u:p@tcp(h:3306)/n?parseTime=true"},
		{false, true, "u:p@tcp(h:3306)/n?parseTime=true"},
		{true, false, "u:p@tcp(h:3306)/n?parseTime=true&tls=true"},
		{true, true, "u:p@tcp(h:3306)/n?parseTime=true&tls=skip-verify"},
	} {
		d := Database{Host: "h", User: "u", Password: "p", Name: "n", TLS: tc.tls, TLSSkipVerify: tc.skipVerify}
		if got := d.DSN(); got != tc.want {
			t.Errorf("tls=%v skipVerify=%v: DSN() = %q, want %q", tc.tls, tc.skipVerify, got, tc.want)
		}
	}
}

func TestDatabaseLiftTLSParam(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		params, wantParams string
		wantTLS, wantSkip  bool
	}{
		{"tls=true&timeout=10s", timeoutParam, true, false},
		{"tls=skip-verify", "", true, true},
		{"tls=false", "", false, false},
		{"tls=preferred", "tls=preferred", false, false},
		{timeoutParam, timeoutParam, false, false},
	} {
		d := Database{Params: tc.params}
		d.LiftTLSParam()

		if d.Params != tc.wantParams || d.TLS != tc.wantTLS || d.TLSSkipVerify != tc.wantSkip {
			t.Errorf("%q: got params=%q tls=%v skip=%v, want %q %v %v",
				tc.params, d.Params, d.TLS, d.TLSSkipVerify, tc.wantParams, tc.wantTLS, tc.wantSkip)
		}
	}
}

func TestDatabaseDriverConfigCustomCA(t *testing.T) {
	t.Parallel()

	caPEM := selfSignedCAPEM(t)
	d := Database{Host: "db.example", User: "u", Name: "n", TLS: true, TLSCACert: caPEM}

	cfg, err := d.DriverConfig()
	if err != nil {
		t.Fatalf("DriverConfig: %v", err)
	}

	if cfg.TLS == nil || cfg.TLS.RootCAs == nil || cfg.TLS.InsecureSkipVerify || cfg.TLS.ServerName != d.Host {
		t.Errorf("TLS config = %+v, want custom roots verifying %q", cfg.TLS, d.Host)
	}

	// skip-verify ignores the CA rather than half-applying it.
	d.TLSSkipVerify = true

	cfg, err = d.DriverConfig()
	if err != nil {
		t.Fatalf("DriverConfig (skip-verify): %v", err)
	}

	if cfg.TLS == nil || !cfg.TLS.InsecureSkipVerify || cfg.TLS.RootCAs != nil {
		t.Errorf("skip-verify TLS config = %+v", cfg.TLS)
	}

	d.TLSSkipVerify, d.TLSCACert = false, "not a certificate"
	if err := d.ValidateConnection(); err == nil {
		t.Error("expected error for invalid CA PEM")
	}
}

func selfSignedCAPEM(t *testing.T) string {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test CA"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}
