// Package config provides the configuration of control.
package config

import (
	"context"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/valentin-kaiser/go-core/apperror"
	"github.com/valentin-kaiser/go-core/config"
	"github.com/valentin-kaiser/go-core/flag"
	"github.com/valentin-kaiser/go-core/logging/log"
	"github.com/valentin-kaiser/go-core/queue"
)

// Job types, they select the restic command.
const (
	TypeBackup = "backup"
	TypeForget = "forget"
	TypeCheck  = "check"
)

// RestoreJob is the reserved name of restore runs, which are never scheduled.
const RestoreJob = "restore"

// cron only validates schedules, it never runs anything.
var cron = queue.NewTaskScheduler()

var jobName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Job describes one scheduled restic job. Repository, password file and source default to the global
// settings, so jobs can back up different directories to different repositories.
type Job struct {
	Name           string `usage:"unique name of the job, lower case letters, digits, - and _"`
	Type           string `usage:"what the job does: backup, forget or check"`
	Enabled        bool   `usage:"schedule this job"`
	Schedule       string `usage:"cron expression (minute hour day month weekday)"`
	Args           string `usage:"additional restic arguments, split like a shell would"`
	TimeoutMinutes int    `usage:"abort the run after this many minutes, 0 disables the timeout"`
	MaxAgeHours    int    `usage:"health check fails when no run succeeded within this many hours, 0 disables"`
	Source         string `usage:"directory to back up, backup jobs only, defaults to the global source"`
	Repository     string `usage:"restic repository, defaults to the global repository"`
	PasswordFile   string `usage:"file containing the repository password, defaults to the global password file"`
	PasswordSecret string `usage:"name of the stored secret that holds the repository password, replaces the password file"`
}

// MountCheck guards against writing into an unmounted repository directory.
type MountCheck struct {
	Path       string `usage:"local repository directory that must exist before any job runs, empty disables the check"`
	MarkerFile string `usage:"file that must exist inside the path (e.g. .mounted), empty only checks the path"`
}

// API configures the management API. Calls need the password as bearer credential. Without authentication bind it to loopback.
// The web portal is a separate process (control-portal) that talks to it.
type API struct {
	Enabled         bool   `usage:"serve the management API, required by the CLI and the web UI"`
	Address         string `usage:"address to bind the API to"`
	Port            int    `usage:"port to bind the API to"`
	TLS             bool   `usage:"serve HTTPS, a self-signed certificate is generated when no certificate files exist"`
	CertificateFile string `usage:"path to the TLS certificate file, relative to the data directory, generated (self-signed) when it does not exist"`
	KeyFile         string `usage:"path to the TLS key file, relative to the data directory"`

	ClientCAFile      string `usage:"CA file for mutual TLS, clients must then present a certificate signed by it"`
	RequireClientCert bool   `usage:"refuse clients without a certificate, needs client-ca-file"`
	PasswordFile      string `usage:"file with the password of the API, the same one the portal signs in with (a Docker secret), read again when it changes"`
	DisableAuth       bool   `usage:"serve the API without a password, anyone who can reach it can run jobs and restore files"`
}

// Run modes. Standalone backs up one source to one repository with at most one job per type and takes the
// repository password from a file, it serves no API. UI serves the management API for the portal, runs any number of
// jobs and takes every repository password from the stored secrets.
const (
	ModeStandalone = "standalone"
	ModePortal     = "portal"
)

// Config is the complete control configuration.
type Config struct {
	LogLevel        int        `usage:"(-1 = trace, 0 = debug, 1 = info, 2 = warn, 3 = error)"`
	Mode            string     `usage:"standalone (one source, one repository, one job per type, password file, no API) or ui (management API for the portal, many jobs, stored secrets)"`
	Repository      string     `usage:"restic repository"`
	PasswordFile    string     `usage:"file containing the repository password"`
	PasswordSecret  string     `usage:"name of the stored secret that holds the repository password, replaces the password file"`
	MasterKeyFile   string     `usage:"file with the key that encrypts the stored secrets (base64, 32 bytes), created in the data directory when empty"`
	Source          string     `usage:"directory to back up"`
	Compression     string     `usage:"restic compression mode (auto, off, max)"`
	CacheDir        string     `usage:"restic cache directory"`
	Nice            int        `usage:"niceness of restic processes (0-19)"`
	ReadConcurrency int        `usage:"number of files restic reads in parallel, 0 uses the restic default"`
	PackSize        int        `usage:"target restic pack size in MiB, 0 uses the restic default"`
	OneFileSystem   bool       `usage:"do not cross filesystem boundaries while backing up"`
	LockRetry       string     `usage:"how long restic waits for a repository lock"`
	StopGraceSecs   int        `usage:"seconds a running job may finish after a shutdown signal"`
	HistoryLimit    int        `usage:"number of runs kept in the history"`
	RestoreRoot     string     `usage:"restores are only allowed below this directory"`
	WebhookURL      string     `usage:"URL pinged with /start, /fail or no suffix on success (healthchecks.io compatible)"`
	Jobs            []Job      `usage:"jobs, a list of name, type, schedule, ..."`
	MountCheck      MountCheck `usage:"repository mount guard"`
	API             API        `usage:"management API used by the CLI and the web UI"`
}

// Default returns the default configuration. The run mode is the one setting read from the environment here, because
// the defaults of the jobs and the password depend on it.
//
// Portal mode has the same three default jobs. It takes the repository password from the stored secret "backup"
// instead of a password file: create that secret in the portal, or point passwordSecret at another one.
func Default() *Config {
	c := &Config{
		LogLevel:        1,
		Mode:            mode(),
		Repository:      "/backup/",
		PasswordFile:    "/run/secrets/backup_password",
		Source:          "/source/",
		Compression:     "auto",
		CacheDir:        "/var/cache/restic",
		Nice:            10,
		ReadConcurrency: 0,
		PackSize:        0,
		OneFileSystem:   false,
		LockRetry:       "5m",
		StopGraceSecs:   60,
		HistoryLimit:    500,
		RestoreRoot:     "/restore",
		Jobs: []Job{
			{
				Name:        "backup",
				Type:        TypeBackup,
				Enabled:     true,
				Schedule:    "0 0 * * *",
				Args:        "--exclude-caches --exclude-if-present=.nobackup --exclude-file=/.backupignore",
				MaxAgeHours: 26,
			},
			{
				Name:     "forget",
				Type:     TypeForget,
				Enabled:  true,
				Schedule: "0 3 * * 0",
				Args:     "--prune --keep-last 7 --keep-daily 14 --keep-weekly 4 --keep-monthly 6 --keep-yearly 1",
			},
			{
				Name:           "check",
				Type:           TypeCheck,
				Enabled:        true,
				Schedule:       "0 5 * * 3",
				Args:           "--read-data-subset=5%",
				TimeoutMinutes: 360,
			},
		},
		API: API{
			Enabled:         true,
			Address:         "127.0.0.1",
			Port:            8080,
			TLS:             false,
			CertificateFile: "certs/control.cert",
			KeyFile:         "certs/control.key",
		},
	}
	if c.Portal() {
		c.PasswordFile, c.PasswordSecret, c.API.PasswordFile = "", "backup", "/run/secrets/portal_password"
	}
	return c
}

// Validate implements the go-core config interface.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.Repository) == "" {
		return apperror.NewError("repository must not be empty")
	}
	if strings.TrimSpace(c.Source) == "" {
		return apperror.NewError("source must not be empty")
	}
	if c.Nice < 0 || c.Nice > 19 {
		return apperror.NewError("nice must be between 0 and 19")
	}
	if c.ReadConcurrency < 0 || c.PackSize < 0 {
		return apperror.NewError("readConcurrency and packSizeMiB must not be negative")
	}
	if c.HistoryLimit < 10 {
		return apperror.NewError("historyLimit must be at least 10")
	}
	if c.PasswordSecret != "" && !jobName.MatchString(c.PasswordSecret) {
		return apperror.NewErrorf("passwordSecret %q must be 1-32 lower case letters, digits, - or _", c.PasswordSecret)
	}
	switch c.Mode {
	case "", ModeStandalone, ModePortal:
	default:
		return apperror.NewErrorf("mode %q must be %s or %s", c.Mode, ModeStandalone, ModePortal)
	}
	if !c.Portal() && c.PasswordSecret != "" {
		return apperror.NewError("passwordSecret needs mode portal, standalone mode takes the password from passwordFile")
	}
	if c.StopGraceSecs < 0 {
		return apperror.NewError("stopGraceSecs must not be negative")
	}
	switch c.Compression {
	case "auto", "off", "max":
	default:
		return apperror.NewErrorf("compression %q must be auto, off or max", c.Compression)
	}
	seen, types := map[string]bool{}, map[string]bool{}
	for i, j := range c.Jobs {
		if !jobName.MatchString(j.Name) || j.Name == RestoreJob {
			return apperror.NewErrorf("jobs[%d].name %q must be 1-32 lower case letters, digits, - or _ and not %q", i, j.Name, RestoreJob)
		}
		if seen[j.Name] {
			return apperror.NewErrorf("job name %q is used twice", j.Name)
		}
		seen[j.Name] = true
		switch j.Type {
		case TypeBackup, TypeForget, TypeCheck:
		default:
			return apperror.NewErrorf("job %s: type %q must be backup, forget or check", j.Name, j.Type)
		}
		if j.Enabled && strings.TrimSpace(j.Schedule) == "" {
			return apperror.NewErrorf("job %s: schedule must not be empty when the job is enabled", j.Name)
		}
		if strings.TrimSpace(j.Schedule) != "" {
			if err := cron.ValidateCronSpec(j.Schedule); err != nil {
				return apperror.NewErrorf("job %s: invalid schedule %q", j.Name, j.Schedule).AddError(err)
			}
		}
		if j.TimeoutMinutes < 0 || j.MaxAgeHours < 0 {
			return apperror.NewErrorf("job %s: timeoutMinutes and maxAgeHours must not be negative", j.Name)
		}
		if j.PasswordSecret != "" && !jobName.MatchString(j.PasswordSecret) {
			return apperror.NewErrorf("job %s: passwordSecret %q must be 1-32 lower case letters, digits, - or _", j.Name, j.PasswordSecret)
		}
		if j.PasswordSecret != "" && j.PasswordFile != "" {
			return apperror.NewErrorf("job %s: set either passwordFile or passwordSecret, not both", j.Name)
		}
		if j.Source != "" && j.Type != TypeBackup {
			return apperror.NewErrorf("job %s: source is only valid for backup jobs", j.Name)
		}
		if c.Portal() {
			if jc := c.ForJob(j); jc.PasswordFile != "" {
				return apperror.NewErrorf("job %s: password files are not used in portal mode, create a secret in the portal and set passwordSecret (or run in standalone mode)", j.Name)
			} else if jc.PasswordSecret == "" {
				return apperror.NewErrorf("job %s: passwordSecret is required in portal mode", j.Name)
			}
			continue
		}
		// Standalone: one source and one repository for everything, one job per type.
		if j.Repository != "" || j.Source != "" || j.PasswordFile != "" || j.PasswordSecret != "" {
			return apperror.NewErrorf("job %s: repository, source and password are global in standalone mode, use mode portal for per job settings", j.Name)
		}
		if types[j.Type] {
			return apperror.NewErrorf("job %s: standalone mode allows one %s job, use mode portal for more", j.Name, j.Type)
		}
		types[j.Type] = true
	}
	if c.Portal() && c.PasswordFile != "" {
		return apperror.NewError("passwordFile is not used in portal mode, create a secret in the portal and set passwordSecret (or run in standalone mode)")
	}
	if c.API.Enabled {
		if c.API.Port < 1 || c.API.Port > 65535 {
			return apperror.NewError("api.port must be between 1 and 65535")
		}
		if c.API.TLS && (strings.TrimSpace(c.API.CertificateFile) == "" || strings.TrimSpace(c.API.KeyFile) == "") {
			return apperror.NewError("api.certificateFile and api.keyFile must not be empty when api.tls is set")
		}
		if c.Portal() && !c.API.DisableAuth && c.API.PasswordFile == "" {
			return apperror.NewError("api.passwordFile is required: the API is protected by the password of the portal")
		}
	}
	return nil
}

// Addr returns host:port of the API server.
func (c Config) Addr() string {
	return c.API.Address + ":" + strconv.Itoa(c.API.Port)
}

// Init registers and reads the configuration.
func Init() {
	if err := config.Manager().WithName("control").Register(Default()); err != nil {
		log.Fatal().Err(err).Msg("failed to register configuration")
	}
	flag.Init()
	if err := config.Read(); err != nil {
		log.Fatal().Err(err).Msg("failed to read configuration")
	}
}

// Get returns a copy of the current configuration.
func Get() Config {
	c, ok := config.Get().(*Config)
	if !ok || c == nil {
		return *Default()
	}
	return *c
}

var updateMu sync.Mutex

// Update changes a copy of the configuration with change, validates and persists it. The OnChange
// handlers run before it returns, so the new jobs are scheduled when it does.
func Update(change func(*Config) error) error {
	updateMu.Lock()
	defer updateMu.Unlock()
	c := Get()
	c.Jobs = slices.Clone(c.Jobs)
	if err := change(&c); err != nil {
		return err
	}
	return config.Write(&c)
}

// Watch starts watching the configuration file.
func Watch(ctx context.Context) {
	if err := config.StartWatch(ctx); err != nil {
		log.Error().Err(err).Msg("failed to start configuration watch")
	}
}

// Stop stops watching the configuration file.
func Stop() { config.StopWatch() }

// OnChange registers a typed change handler.
func OnChange(f func(o, n *Config) error) {
	config.OnChange(func(o, n config.Config) error {
		oc, ook := o.(*Config)
		nc, nok := n.(*Config)
		if !ook || !nok || oc == nil || nc == nil {
			return apperror.NewError("configuration has unexpected type")
		}
		return f(oc, nc)
	})
}

// Job returns the job with the given name.
func (c Config) Job(name string) (Job, bool) {
	for _, j := range c.Jobs {
		if j.Name == name {
			return j, true
		}
	}
	return Job{}, false
}

// Portal reports whether the management API and the stored secrets are used. Anything but portal is standalone.
func (c Config) Portal() bool { return c.Mode == ModePortal }

// ForJob returns the configuration with the repository, password and source of the job applied.
func (c Config) ForJob(j Job) Config {
	if j.Repository != "" {
		c.Repository = j.Repository
	}
	// A password of the job replaces the one of the global settings, whichever way it is given.
	if j.PasswordFile != "" || j.PasswordSecret != "" {
		c.PasswordFile, c.PasswordSecret = j.PasswordFile, j.PasswordSecret
	}
	if j.Source != "" {
		c.Source = j.Source
	}
	return c
}

// RepositoryKey identifies the repository a job works on, jobs with the same key never run at the same time.
func (c Config) RepositoryKey(j Job) string {
	return c.ForJob(j).Repository
}

// mode is the run mode from CONTROL_MODE, standalone when it is not set.
func mode() string {
	if v := strings.TrimSpace(os.Getenv("CONTROL_MODE")); v != "" {
		return v
	}
	return ModeStandalone
}
