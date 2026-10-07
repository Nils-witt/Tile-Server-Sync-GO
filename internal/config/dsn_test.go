package config

import "testing"

func TestDatabaseDSNRoundtrip(t *testing.T) {
	t.Parallel()

	d := Database{Host: "db.example", User: "u", Password: "p@ss:w/rd", Name: "geo", Params: "tls=true"}

	if err := d.validateConnection(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	want := "u:p@ss:w/rd@tcp(db.example:3306)/geo?parseTime=true&tls=true"
	if got := d.DSN(); got != want {
		t.Errorf("DSN() = %q, want %q", got, want)
	}

	parsed, err := DatabaseFromDSN(d.DSN())
	if err != nil {
		t.Fatalf("DatabaseFromDSN: %v", err)
	}

	if parsed.Host != d.Host || parsed.Port != defaultDatabasePort || parsed.User != d.User ||
		parsed.Password != d.Password || parsed.Name != d.Name || parsed.Params != d.Params {
		t.Errorf("roundtrip = %+v, want %+v", parsed, d)
	}
}

func TestDatabaseValidateConnection(t *testing.T) {
	t.Parallel()

	valid := Database{Host: "h", User: "u", Name: "n"}

	for name, mutate := range map[string]func(*Database){
		"missing host": func(d *Database) { d.Host = "" },
		"missing user": func(d *Database) { d.User = "" },
		"missing name": func(d *Database) { d.Name = "" },
		"bad port":     func(d *Database) { d.Port = 70000 },
		"bad params":   func(d *Database) { d.Params = "timeout=notaduration" },
	} {
		d := valid
		mutate(&d)

		if err := d.validateConnection(); err == nil {
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
