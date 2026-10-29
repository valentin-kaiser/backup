package config

import (
	"path/filepath"
	"testing"
)

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateJobs(t *testing.T) {
	job := func(name, typ string) Job {
		j := Job{Name: name, Type: typ, Enabled: true, Schedule: "0 0 * * *", Repository: "main"}
		if typ == TypeBackup {
			j.Source = "/src"
		}
		return j
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
		{"no schedule", []Job{{Name: "a", Type: TypeBackup, Enabled: true, Repository: "main", Source: "/src"}}, false},
		{"disabled without schedule", []Job{{Name: "a", Type: TypeBackup, Repository: "main", Source: "/src"}}, true},
		{"source on forget", []Job{{Name: "a", Type: TypeForget, Repository: "main", Source: "/x"}}, false},
		{"negative timeout", []Job{{Name: "a", Type: TypeBackup, Repository: "main", Source: "/src", TimeoutMinutes: -1}}, false},
		{"no repository", []Job{{Name: "a", Type: TypeBackup, Source: "/src"}}, false},
		{"unknown repository", []Job{{Name: "a", Type: TypeForget, Repository: "nope"}}, false},
		{"backup without source", []Job{{Name: "a", Type: TypeBackup, Repository: "main"}}, false},
		{"relative source", []Job{{Name: "a", Type: TypeBackup, Repository: "main", Source: "src"}}, false},
		{"mounts on a backup job", []Job{{Name: "a", Type: TypeBackup, Repository: "main", Source: "/src", Mounts: []MountCheck{{Path: "/x"}}}}, false},
	}
	for _, tt := range tests {
		c := portalConfig()
		c.Jobs = tt.jobs
		if err := c.Validate(); (err == nil) != tt.ok {
			t.Errorf("%s: err = %v", tt.name, err)
		}
	}
}

func TestForJobResolvesTheRepository(t *testing.T) {
	c := portalConfig()
	c.Repositories = append(c.Repositories, Repository{
		Name: "usb", Path: "/backup/usb/vw", PasswordSecret: "vw",
		Mount: MountCheck{Path: "/backup/usb", MarkerFile: ".mounted"}, RestoreRoot: "/restore/vw",
	})

	got := c.ForJob(Job{Name: "vw", Type: TypeBackup, Repository: "usb", Source: "/vw"})
	if got.Repository != "/backup/usb/vw" || got.PasswordSecret != "vw" || got.Source != "/vw" || got.RestoreRoot != "/restore/vw" {
		t.Errorf("repository not applied: %+v", got)
	}
	if g := got.MountGuards(); len(g) != 1 || g[0].Path != "/backup/usb" || g[0].MarkerFile != ".mounted" {
		t.Errorf("the mount guard of the repository must apply: %+v", g)
	}
	main := c.ForJob(Job{Name: "x", Type: TypeCheck, Repository: "main"})
	if main.Repository != "/repo/main" || main.PasswordSecret != "main" || main.RestoreRoot != DefaultRestoreRoot || len(main.MountGuards()) != 0 {
		t.Errorf("the repositories must not leak into each other: %+v", main)
	}
	if c.Repository != "" || c.PasswordSecret != "" {
		t.Error("the original configuration must not change")
	}
}

func TestForJobOfSyncUsesItsOwnMounts(t *testing.T) {
	c := portalConfig()
	got := c.ForJob(Job{Type: TypeSync, Mounts: []MountCheck{{Path: "/a"}, {Path: "/b", MarkerFile: ".m"}}})
	if g := got.MountGuards(); len(g) != 2 || g[1].MarkerFile != ".m" {
		t.Errorf("guards = %+v", g)
	}
}

func TestStandaloneHasOneImplicitRepository(t *testing.T) {
	c := *Default()
	c.Repository, c.RestoreRoot = "/repo", "/restore"
	repos := c.Repos()
	if len(repos) != 1 || repos[0].Name != DefaultRepository || repos[0].Path != "/repo" {
		t.Fatalf("repos = %+v", repos)
	}
	if got := c.ForJob(Job{Type: TypeBackup}); got.Repository != "/repo" || got.PasswordFile != c.PasswordFile {
		t.Errorf("standalone jobs use the top level settings: %+v", got)
	}
	if got := c.JobsOf(DefaultRepository); len(got) != 3 {
		t.Errorf("the three default jobs work on the implicit repository, got %v", got)
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

func TestRepositoryValidation(t *testing.T) {
	if err := func() error { c := portalConfig(); return c.Validate() }(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Config){
		"bad name":   func(c *Config) { c.Repositories[0].Name = "Bad Name" },
		"empty name": func(c *Config) { c.Repositories[0].Name = "" },
		"duplicate name": func(c *Config) {
			c.Repositories = append(c.Repositories, Repository{Name: "main", Path: "/other", PasswordSecret: "x"})
		},
		"duplicate path": func(c *Config) {
			c.Repositories = append(c.Repositories, Repository{Name: "two", Path: "/repo/main", PasswordSecret: "x"})
		},
		"no path":             func(c *Config) { c.Repositories[0].Path = " " },
		"no secret":           func(c *Config) { c.Repositories[0].PasswordSecret = "" },
		"bad secret":          func(c *Config) { c.Repositories[0].PasswordSecret = "../etc" },
		"relative mount":      func(c *Config) { c.Repositories[0].Mount.Path = "mnt" },
		"marker without path": func(c *Config) { c.Repositories[0].Mount.MarkerFile = ".mounted" },
		"relative restore":    func(c *Config) { c.Repositories[0].RestoreRoot = "restore" },
		"global repository":   func(c *Config) { c.Repository = "/repo" },
		"global password":     func(c *Config) { c.PasswordFile = "/pw" },
		"global source":       func(c *Config) { c.Source = "/src" },
		"global mount":        func(c *Config) { c.MountCheck.Path = "/mnt" },
		"global mounts":       func(c *Config) { c.MountChecks = []MountCheck{{Path: "/mnt"}} },
		"global restore":      func(c *Config) { c.RestoreRoot = "/restore" },
	} {
		c := portalConfig()
		mutate(&c)
		if c.Validate() == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}

// portalConfig is a valid portal mode configuration with one repository, "main", that uses the secret "main".
func portalConfig() Config {
	c := *Default()
	c.Mode = ModePortal
	c.Repository, c.PasswordFile, c.PasswordSecret, c.Source, c.RestoreRoot, c.Jobs = "", "", "", "", "", nil
	c.Repositories = []Repository{{Name: "main", Path: "/repo/main", PasswordSecret: "main"}}
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

func TestPortalDefaultStartsEmpty(t *testing.T) {
	t.Setenv("CONTROL_MODE", ModePortal)
	c := Default()
	if !c.Portal() || len(c.Jobs) != 0 || len(c.Repositories) != 0 {
		t.Errorf("portal mode starts without repositories and jobs: %+v", c)
	}
	if c.Repository != "" || c.PasswordFile != "" || c.Source != "" || c.RestoreRoot != "" {
		t.Errorf("nothing is global in portal mode: %+v", c)
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
		"one per type":   {[]Job{job("a", TypeBackup), job("b", TypeForget), job("c", TypeCheck)}, true},
		"two backups":    {[]Job{job("a", TypeBackup), job("b", TypeBackup)}, false},
		"own repository": {[]Job{{Name: "a", Type: TypeBackup, Repository: "main"}}, false},
		"own source":     {[]Job{{Name: "a", Type: TypeBackup, Source: "/other"}}, false},
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
		t.Error("a secret needs portal mode")
	}
	c = Default()
	c.Repositories = []Repository{{Name: "a", Path: "/a", PasswordSecret: "s"}}
	if c.Validate() == nil {
		t.Error("repositories need portal mode")
	}
	c = Default()
	c.Mode = "cluster"
	if c.Validate() == nil {
		t.Error("an unknown mode must be refused")
	}
}

func TestPortalSyncJobMounts(t *testing.T) {
	abs := func(p string) string { a, _ := filepath.Abs(p); return a }
	c := portalConfig()
	c.Jobs = []Job{{Name: "s", Type: TypeSync, Path1: abs("/a"), Path2: abs("/b"), Mounts: []MountCheck{{Path: abs("/a"), MarkerFile: ".m"}}}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Jobs[0].Mounts = []MountCheck{{Path: "rel"}}
	if c.Validate() == nil {
		t.Error("a relative mount path must be refused")
	}
}

func TestJobsOf(t *testing.T) {
	c := portalConfig()
	c.Repositories = append(c.Repositories, Repository{Name: "other", Path: "/repo/other", PasswordSecret: "o"})
	c.Jobs = []Job{
		{Name: "a", Type: TypeBackup, Repository: "main", Source: "/s"},
		{Name: "b", Type: TypeForget, Repository: "main"},
		{Name: "s", Type: TypeSync, Path1: "/a", Path2: "/b"},
	}
	if got := c.JobsOf("main"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("jobs of main = %v", got)
	}
	if c.UsedByJobs("other") {
		t.Error("other has no jobs")
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

func TestAfterValidation(t *testing.T) {
	base := func(jobs ...Job) Config {
		c := *Default()
		c.Source = "/src"
		c.Jobs = append([]Job{{Name: "backup", Type: TypeBackup, Enabled: true, Schedule: "0 0 * * *"}}, jobs...)
		return c
	}
	cases := map[string]struct {
		jobs []Job
		ok   bool
	}{
		"chain":      {[]Job{{Name: "forget", Type: TypeForget, After: "backup"}, {Name: "check", Type: TypeCheck, After: "forget"}}, true},
		"unknown":    {[]Job{{Name: "forget", Type: TypeForget, After: "nope"}}, false},
		"scheduled":  {[]Job{{Name: "forget", Type: TypeForget, After: "backup", Enabled: true, Schedule: "0 1 * * *"}}, false},
		"self":       {[]Job{{Name: "forget", Type: TypeForget, After: "forget"}}, false},
		"cycle":      {[]Job{{Name: "a", Type: TypeForget, After: "b"}, {Name: "b", Type: TypeCheck, After: "a"}}, false},
		"mirror src": {[]Job{{Name: "s", Type: TypeSync, Path1: "/a", Path2: "peer:x/b", Owner: "x"}, {Name: "forget", Type: TypeForget, After: "s"}}, false},
	}
	for name, tc := range cases {
		c := base(tc.jobs...)
		err := c.Validate()
		if tc.ok && err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
