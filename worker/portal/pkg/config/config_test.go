package config

import "testing"

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Config)
		ok     bool
	}{
		{"defaults", func(*Config) {}, true},
		{"bad port", func(c *Config) { c.Port = 0 }, false},
		{"plain http on all interfaces", func(c *Config) { c.Address, c.TLS = "0.0.0.0", false }, true},
	}
	for _, tt := range tests {
		c := Default()
		tt.change(c)
		if err := c.Validate(); (err == nil) != tt.ok {
			t.Errorf("%s: err = %v", tt.name, err)
		}
	}
}

func TestValidateDaemon(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Config)
		ok     bool
	}{
		{"remote daemon", func(c *Config) { c.Daemon.URL = "https://backup:9443" }, true},
		{"without scheme", func(c *Config) { c.Daemon.URL = "backup:9443" }, false},
		{"another scheme", func(c *Config) { c.Daemon.URL = "ftp://backup" }, false},
		{"empty", func(c *Config) { c.Daemon.URL = "" }, false},
		{"with a path", func(c *Config) { c.Daemon.URL = "http://backup:9080/api" }, false},
		{"with credentials", func(c *Config) { c.Daemon.URL = "http://me:pw@backup:9080" }, false},
		{"bad fingerprint", func(c *Config) { c.Daemon.Fingerprint = "nothex" }, false},
		{"client certificate without key", func(c *Config) { c.Daemon.ClientCertificateFile = "client.pem" }, false},
	}
	for _, tt := range tests {
		c := Default()
		tt.change(c)
		if err := c.Validate(); (err == nil) != tt.ok {
			t.Errorf("%s: err = %v", tt.name, err)
		}
	}
}
