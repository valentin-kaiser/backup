package config

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestValidateGroups(t *testing.T) {
	portal := func(jobs ...Job) *Config {
		c := Default()
		c.Mode, c.PasswordFile, c.PasswordSecret, c.API.Enabled = ModePortal, "", "backup", false
		c.Jobs = jobs
		return c
	}
	member := func(name, typ string) Job { return Job{Name: name, Type: typ, PasswordSecret: "backup"} }
	group := func(members ...string) Job {
		return Job{Name: "nightly", Type: TypeGroup, Enabled: true, Schedule: "0 1 * * *", Members: members}
	}
	tests := map[string]struct {
		jobs []Job
		ok   bool
	}{
		"valid":              {[]Job{member("b", TypeBackup), member("f", TypeForget), group("b", "f")}, true},
		"no members":         {[]Job{member("b", TypeBackup), group()}, false},
		"unknown member":     {[]Job{member("b", TypeBackup), group("b", "nope")}, false},
		"duplicate member":   {[]Job{member("b", TypeBackup), group("b", "b")}, false},
		"nested":             {[]Job{member("b", TypeBackup), {Name: "inner", Type: TypeGroup, Members: []string{"b"}}, group("inner")}, false},
		"member scheduled":   {[]Job{{Name: "b", Type: TypeBackup, Enabled: true, Schedule: "0 0 * * *", PasswordSecret: "backup"}, group("b")}, false},
		"disabled group":     {[]Job{{Name: "b", Type: TypeBackup, Enabled: true, Schedule: "0 0 * * *", PasswordSecret: "backup"}, {Name: "g", Type: TypeGroup, Members: []string{"b"}}}, true},
		"group arguments":    {[]Job{member("b", TypeBackup), {Name: "g", Type: TypeGroup, Members: []string{"b"}, Args: "--x"}}, false},
		"group repository":   {[]Job{member("b", TypeBackup), {Name: "g", Type: TypeGroup, Members: []string{"b"}, Repository: "/r"}}, false},
		"members on backup":  {[]Job{{Name: "b", Type: TypeBackup, PasswordSecret: "backup", Members: []string{"b"}}}, false},
		"continue on backup": {[]Job{{Name: "b", Type: TypeBackup, PasswordSecret: "backup", ContinueOnError: true}}, false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			c := portal(tt.jobs...)
			if err := c.Validate(); (err == nil) != tt.ok {
				t.Errorf("valid = %v, want %v: %v", err == nil, tt.ok, err)
			}
		})
	}
	c := portal(member("b", TypeBackup), member("f", TypeForget), group("b", "f"))
	if got := c.Groups("b"); len(got) != 1 || got[0] != "nightly" {
		t.Errorf("groups of b = %v", got)
	}
	if got := c.Groups("nightly"); len(got) != 0 {
		t.Errorf("a group is in no group: %v", got)
	}
}

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
		{"bad type", []Job{job("a", "mirror")}, false},
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
		if !reflect.DeepEqual(j, standalone.Jobs[i]) {
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

func TestValidateSyncJobs(t *testing.T) {
	abs := func(p string) string { a, _ := filepath.Abs(p); return a }
	a, b := abs("/sync/a"), abs("/sync/b")
	sync := func(mod func(*Job)) Job {
		j := Job{Name: "mirror", Type: TypeSync, Enabled: true, Schedule: "*/5 * * * *", Path1: a, Path2: b}
		if mod != nil {
			mod(&j)
		}
		return j
	}
	tests := []struct {
		name string
		job  Job
		ok   bool
	}{
		{"two local paths", sync(nil), true},
		{"peer", sync(func(j *Job) { j.Path2 = "peer:west/data" }), true},
		{"unknown peer", sync(func(j *Job) { j.Path2 = "peer:east" }), false},
		{"same path", sync(func(j *Job) { j.Path2 = a }), false},
		{"nested path", sync(func(j *Job) { j.Path2 = filepath.Join(a, "sub") }), false},
		{"relative path1", sync(func(j *Job) { j.Path1 = "a" }), false},
		{"relative path2", sync(func(j *Job) { j.Path2 = "b" }), false},
		{"no path2", sync(func(j *Job) { j.Path2 = "" }), false},
		{"conflict policy", sync(func(j *Job) { j.Conflict = ConflictNone; j.ConflictLoser = LoserPathname }), true},
		{"bad conflict", sync(func(j *Job) { j.Conflict = "random" }), false},
		{"bad loser", sync(func(j *Job) { j.ConflictLoser = "random" }), false},
		{"max delete", sync(func(j *Job) { j.MaxDeletePct = 101 }), false},
		{"force", sync(func(j *Job) { j.Force = true }), true},
		{"force on a backup job", Job{Name: "x", Type: TypeBackup, Force: true}, false},
		{"repository", sync(func(j *Job) { j.Repository = "/repo" }), false},
		{"password secret", sync(func(j *Job) { j.PasswordSecret = "main" }), false},
		{"paths on a backup job", Job{Name: "x", Type: TypeBackup, Path1: a}, false},
	}
	for _, mode := range []string{ModeStandalone, ModePortal} {
		for _, tt := range tests {
			c := portalConfig()
			if mode == ModeStandalone {
				c = *Default()
			}
			c.Peers = []Peer{{Name: "west", URL: "https://west:9081", Username: "sync", PasswordFile: "/pw"}}
			c.Jobs = []Job{tt.job}
			if err := c.Validate(); (err == nil) != tt.ok {
				t.Errorf("%s/%s: err = %v", mode, tt.name, err)
			}
		}
	}
}

func TestValidateSyncJobsAreNotLimitedPerType(t *testing.T) {
	c := *Default()
	abs := func(p string) string { a, _ := filepath.Abs(p); return a }
	c.Jobs = []Job{
		{Name: "one", Type: TypeSync, Path1: abs("/a"), Path2: abs("/b")},
		{Name: "two", Type: TypeSync, Path1: abs("/c"), Path2: abs("/d")},
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidatePeersAndSyncServe(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*Config)
		ok   bool
	}{
		{"valid peer", func(c *Config) { c.Peers = []Peer{{Name: "west", URL: "http://west:9081"}} }, true},
		{"duplicate", func(c *Config) { c.Peers = []Peer{{Name: "w", URL: "http://w"}, {Name: "w", URL: "http://w"}} }, false},
		{"hyphen in name", func(c *Config) { c.Peers = []Peer{{Name: "west-1", URL: "http://w"}} }, false},
		{"bad url", func(c *Config) { c.Peers = []Peer{{Name: "w", URL: "ftp://w"}} }, false},
		{"serve needs auth", func(c *Config) { c.SyncServe.Enabled = true }, false},
		{"serve", func(c *Config) { c.SyncServe.Enabled, c.SyncServe.PasswordFile = true, "/pw" }, true},
		{"serve certificate without key", func(c *Config) {
			c.SyncServe.Enabled, c.SyncServe.PasswordFile, c.SyncServe.CertificateFile = true, "/pw", "/c.pem"
		}, false},
	}
	for _, tt := range tests {
		c := *Default()
		c.SyncServe.Path, _ = filepath.Abs("/sync")
		tt.mod(&c)
		if err := c.Validate(); (err == nil) != tt.ok {
			t.Errorf("%s: err = %v", tt.name, err)
		}
	}
}

func TestSyncKeyIgnoresTheOrderOfThePaths(t *testing.T) {
	if SyncKey(Job{Path1: "/a", Path2: "/b"}) != SyncKey(Job{Path1: "/b", Path2: "/a"}) {
		t.Error("the same pair must give the same key")
	}
	c := *Default()
	if got := c.RepositoryKey(Job{Type: TypeSync, Path1: "/a", Path2: "/b"}); got == c.Repository {
		t.Errorf("sync jobs must not lock the repository, got %q", got)
	}
}

func TestValidateUnisonJobs(t *testing.T) {
	abs := func(p string) string { a, _ := filepath.Abs(p); return a }
	a, b := abs("/sync/a"), abs("/sync/b")
	job := func(mod func(*Job)) Job {
		j := Job{Name: "mirror", Type: TypeSync, Engine: EngineUnison, Path1: a, Path2: b}
		if mod != nil {
			mod(&j)
		}
		return j
	}
	tests := []struct {
		name string
		job  Job
		ok   bool
	}{
		{"local", job(nil), true},
		{"path1 wins", job(func(j *Job) { j.Conflict = ConflictPath1 }), true},
		{"skip conflicts", job(func(j *Job) { j.Conflict = ConflictNone }), true},
		{"larger is rclone only", job(func(j *Job) { j.Conflict = ConflictLarger }), false},
		{"max delete is rclone only", job(func(j *Job) { j.MaxDeletePct = 20 }), false},
		{"force is rclone only", job(func(j *Job) { j.Force = true }), false},
		{"peer with api", job(func(j *Job) { j.Path2 = "peer:west" }), true},
		{"peer without api", job(func(j *Job) { j.Path2 = "peer:east" }), false},
		{"unknown engine", job(func(j *Job) { j.Engine = "rsync" }), false},
		{"engine on a backup job", Job{Name: "x", Type: TypeBackup, Engine: EngineUnison}, false},
	}
	for _, tt := range tests {
		c := portalConfig()
		c.Peers = []Peer{
			{Name: "west", URL: "https://west:9081", APIURL: "https://west:9080"},
			{Name: "east", URL: "https://east:9081"},
		}
		c.Jobs = []Job{tt.job}
		if err := c.Validate(); (err == nil) != tt.ok {
			t.Errorf("%s: err = %v", tt.name, err)
		}
	}
}

func TestSyncServeTunnelNeedsOnlyAPath(t *testing.T) {
	c := *Default()
	c.SyncServe.Tunnel = true
	c.SyncServe.Path, _ = filepath.Abs("/sync")
	if err := c.Validate(); err != nil {
		t.Errorf("the tunnel uses the API for authentication: %v", err)
	}
	c.SyncServe.Path = "sync"
	if err := c.Validate(); err == nil {
		t.Error("the exported path must be absolute")
	}
}
