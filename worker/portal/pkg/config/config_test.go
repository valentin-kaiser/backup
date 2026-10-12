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
		{"remote daemon", func(c *Config) { c.Daemon.URL = "https://backup:9443" }, true},
		{"daemon without scheme", func(c *Config) { c.Daemon.URL = "backup:9443" }, false},
		{"daemon with another scheme", func(c *Config) { c.Daemon.URL = "ftp://backup" }, false},
		{"empty daemon", func(c *Config) { c.Daemon.URL = "" }, false},
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
