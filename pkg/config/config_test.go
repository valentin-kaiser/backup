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
		c := Default()
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
