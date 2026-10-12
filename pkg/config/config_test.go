package config

import "testing"

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateJobs(t *testing.T) {
	job := func(name, typ string) Job {
		return Job{Name: name, Type: typ, Enabled: true, Schedule: "0 0 * * *"}
	}
	tests := []struct {
		name string
		jobs []Job
		ok   bool
	}{
		{"none", nil, true},
		{"several", []Job{job("photos", TypeBackup), job("docs", TypeBackup), job("prune-docs", TypeForget)}, true},
		{"duplicate", []Job{job("a", TypeBackup), job("a", TypeCheck)}, false},
		{"reserved", []Job{job("restore", TypeBackup)}, false},
		{"bad name", []Job{job("My Job", TypeBackup)}, false},
		{"empty name", []Job{job("", TypeBackup)}, false},
		{"bad type", []Job{job("a", "sync")}, false},
		{"no schedule", []Job{{Name: "a", Type: TypeBackup, Enabled: true}}, false},
		{"disabled without schedule", []Job{{Name: "a", Type: TypeBackup}}, true},
		{"source on forget", []Job{{Name: "a", Type: TypeForget, Source: "/x"}}, false},
		{"negative timeout", []Job{{Name: "a", Type: TypeBackup, TimeoutMinutes: -1}}, false},
	}
	for _, tt := range tests {
		c := portalConfig()
		c.Jobs = tt.jobs
		if err := c.Validate(); (err == nil) != tt.ok {
			t.Errorf("%s: err = %v", tt.name, err)
		}
	}
}

func TestForJobAppliesOverrides(t *testing.T) {
	c := Default()
	c.Repository, c.PasswordFile, c.Source = "/repo", "/pw", "/src"

	got := c.ForJob(Job{Name: "photos", Type: TypeBackup, Repository: "/other", PasswordFile: "/pw2", Source: "/photos"})
	if got.Repository != "/other" || got.PasswordFile != "/pw2" || got.Source != "/photos" {
		t.Errorf("overrides not applied: %+v", got)
	}
	if got := c.ForJob(Job{Name: "x", Type: TypeCheck}); got.Repository != "/repo" || got.PasswordFile != "/pw" || got.Source != "/src" {
		t.Errorf("defaults not kept: %+v", got)
	}
	if c.Repository != "/repo" {
		t.Error("the original configuration must not change")
	}
}

func TestJobLookup(t *testing.T) {
	c := Default()
	if j, ok := c.Job("backup"); !ok || j.Type != TypeBackup {
		t.Errorf("job = %+v, %v", j, ok)
	}
	if _, ok := c.Job("nope"); ok {
		t.Error("unknown job found")
	}
}

func TestPasswordSecretPrecedence(t *testing.T) {
	g := *Default()
	g.PasswordFile = "/global/file"
	g.PasswordSecret = "global-secret"

	if c := g.ForJob(Job{}); c.PasswordSecret != "global-secret" || c.PasswordFile != "/global/file" {
		t.Errorf("a job without its own password inherits both: %+v", c)
	}
	if c := g.ForJob(Job{PasswordSecret: "mine"}); c.PasswordSecret != "mine" || c.PasswordFile != "" {
		t.Errorf("the secret of the job replaces the global password: %q %q", c.PasswordSecret, c.PasswordFile)
	}
	if c := g.ForJob(Job{PasswordFile: "/mine"}); c.PasswordFile != "/mine" || c.PasswordSecret != "" {
		t.Errorf("the file of the job replaces the global secret: %q %q", c.PasswordFile, c.PasswordSecret)
	}
}

func TestPasswordSecretValidation(t *testing.T) {
	c := portalConfig()
	c.Jobs = []Job{{Name: "a", Type: TypeBackup, PasswordSecret: "ok_name-1"}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Config){
		"bad job secret":    func(c *Config) { c.Jobs[0].PasswordSecret = "Bad Name" },
		"file and secret":   func(c *Config) { c.Jobs[0].PasswordFile = "/f" },
		"bad global secret": func(c *Config) { c.PasswordSecret = "../etc" },
	} {
		n := portalConfig()
		n.Jobs = []Job{{Name: "a", Type: TypeBackup, PasswordSecret: "ok_name-1"}}
		mutate(&n)
		if n.Validate() == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}

// portalConfig is a valid portal mode configuration whose jobs take their password from the secret "main".
func portalConfig() Config {
	c := *Default()
	c.Mode, c.PasswordFile, c.PasswordSecret = ModePortal, "", "main"
	c.API.PasswordFile = "/run/secrets/portal_password"
	return c
}

func TestStandaloneIsTheDefaultMode(t *testing.T) {
	c := Default()
	if c.Portal() || c.Mode != ModeStandalone {
		t.Errorf("mode = %q, want standalone", c.Mode)
	}
	if len(c.Jobs) != 3 || c.PasswordFile == "" {
		t.Errorf("standalone seeds the three jobs and a password file: %d jobs, file %q", len(c.Jobs), c.PasswordFile)
	}
}

func TestPortalDefaultHasTheDefaultJobsAndASecret(t *testing.T) {
	standalone := Default()
	t.Setenv("CONTROL_MODE", ModePortal)
	c := Default()
	if !c.Portal() || c.PasswordFile != "" || c.PasswordSecret != "backup" {
		t.Errorf("portal mode takes its password from a secret: %+v", c)
	}
	if len(c.Jobs) != 3 || len(c.Jobs) != len(standalone.Jobs) {
		t.Fatalf("portal mode is seeded with the default jobs: %+v", c.Jobs)
	}
	for i, j := range c.Jobs {
		if j != standalone.Jobs[i] {
			t.Errorf("job %s differs from the standalone job: %+v", j.Name, j)
		}
	}
	if err := c.Validate(); err != nil {
		t.Errorf("the default portal configuration must be valid: %v", err)
	}
}

func TestStandaloneLimits(t *testing.T) {
	job := func(name, typ string) Job { return Job{Name: name, Type: typ, Enabled: true, Schedule: "0 0 * * *"} }
	for name, tt := range map[string]struct {
		jobs []Job
		ok   bool
	}{
		"one per type":     {[]Job{job("a", TypeBackup), job("b", TypeForget), job("c", TypeCheck)}, true},
		"two backups":      {[]Job{job("a", TypeBackup), job("b", TypeBackup)}, false},
		"own repository":   {[]Job{{Name: "a", Type: TypeBackup, Repository: "/other"}}, false},
		"own source":       {[]Job{{Name: "a", Type: TypeBackup, Source: "/other"}}, false},
		"own password":     {[]Job{{Name: "a", Type: TypeBackup, PasswordFile: "/pw"}}, false},
		"secret reference": {[]Job{{Name: "a", Type: TypeBackup, PasswordSecret: "s"}}, false},
	} {
		c := Default()
		c.Jobs = tt.jobs
		if err := c.Validate(); (err == nil) != tt.ok {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	c := Default()
	c.PasswordSecret = "s"
	if c.Validate() == nil {
		t.Error("a global secret needs portal mode")
	}
	c = Default()
	c.Mode = "cluster"
	if c.Validate() == nil {
		t.Error("an unknown mode must be refused")
	}
}

func TestPortalRequiresSecrets(t *testing.T) {
	job := Job{Name: "a", Type: TypeBackup, Enabled: true, Schedule: "0 0 * * *"}
	for name, mutate := range map[string]func(*Config){
		"inherited secret": func(c *Config) { c.Jobs = []Job{job} },
		"own secret":       func(c *Config) { c.PasswordSecret = ""; j := job; j.PasswordSecret = "own"; c.Jobs = []Job{j} },
		"no secret":        func(c *Config) { c.PasswordSecret = ""; c.Jobs = []Job{job} },
		"job file":         func(c *Config) { j := job; j.PasswordFile = "/pw"; c.Jobs = []Job{j} },
		"global file":      func(c *Config) { c.PasswordFile = "/pw" },
	} {
		c := portalConfig()
		mutate(&c)
		wantOK := name == "inherited secret" || name == "own secret"
		if err := c.Validate(); (err == nil) != wantOK {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
