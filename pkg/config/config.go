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
}

// MountCheck guards against writing into an unmounted repository directory.
type MountCheck struct {
	Path       string `usage:"local repository directory that must exist before any job runs, empty disables the check"`
	MarkerFile string `usage:"file that must exist inside the path (e.g. .mounted), empty only checks the path"`
}

// API configures the management API. It has no authentication, bind it to loopback or a private network.
// The web portal is a separate process (control-portal) that talks to it.
type API struct {
	Enabled         bool   `usage:"serve the management API, required by the CLI and the web UI"`
	Address         string `usage:"address to bind the API to"`
	Port            int    `usage:"port to bind the API to"`
	TLS             bool   `usage:"serve HTTPS, a self-signed certificate is generated when no certificate files exist"`
	CertificateFile string `usage:"path to the TLS certificate file"`
	KeyFile         string `usage:"path to the TLS key file"`
}

// Config is the complete control configuration.
type Config struct {
	LogLevel        int        `usage:"(-1 = trace, 0 = debug, 1 = info, 2 = warn, 3 = error)"`
	Repository      string     `usage:"restic repository"`
	PasswordFile    string     `usage:"file containing the repository password"`
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

func env(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

// Default returns the default configuration. Legacy environment variables of
// the shell based image seed the defaults so existing deployments keep working.
func Default() *Config {
	return &Config{
		LogLevel:        1,
		Repository:      env("RESTIC_REPOSITORY", "/backup/"),
		PasswordFile:    env("RESTIC_PASSWORD_FILE", "/run/secrets/backup_password"),
		Source:          env("RESTIC_SOURCE", "/source/"),
		Compression:     env("RESTIC_COMPRESSION", "auto"),
		CacheDir:        env("RESTIC_CACHE_DIR", "/var/cache/restic"),
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
				Schedule:    env("BACKUP_SCHEDULE", "0 0 * * *"),
				Args:        env("RESTIC_BACKUP_ARGS", "--exclude-caches --exclude-if-present=.nobackup --exclude-file=/.backupignore"),
				MaxAgeHours: 26,
			},
			{
				Name:     "forget",
				Type:     TypeForget,
				Enabled:  true,
				Schedule: env("FORGET_SCHEDULE", "0 3 * * 0"),
				Args:     env("RESTIC_FORGET_ARGS", "--prune --keep-last 7 --keep-daily 14 --keep-weekly 4 --keep-monthly 6 --keep-yearly 1"),
			},
			{
				Name:           "check",
				Type:           TypeCheck,
				Enabled:        true,
				Schedule:       "0 5 * * 3",
				Args:           "--read-data-subset=5%",
				TimeoutMinutes: 360,
			},
		}, MountCheck: MountCheck{
			Path:       env("CONTROL_MOUNT_CHECK_PATH", ""),
			MarkerFile: env("CONTROL_MOUNT_CHECK_MARKER", ""),
		},
		API: API{
			Enabled: true,
			Address: "127.0.0.1",
			Port:    8080,
			TLS:     false,
		},
	}
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
	if c.StopGraceSecs < 0 {
		return apperror.NewError("stopGraceSecs must not be negative")
	}
	switch c.Compression {
	case "auto", "off", "max":
	default:
		return apperror.NewErrorf("compression %q must be auto, off or max", c.Compression)
	}
	seen := map[string]bool{}
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
		if j.Source != "" && j.Type != TypeBackup {
			return apperror.NewErrorf("job %s: source is only valid for backup jobs", j.Name)
		}
	}
	if c.API.Enabled {
		if c.API.Port < 1 || c.API.Port > 65535 {
			return apperror.NewError("api.port must be between 1 and 65535")
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

// ForJob returns the configuration with the repository, password file and source of the job applied.
func (c Config) ForJob(j Job) Config {
	if j.Repository != "" {
		c.Repository = j.Repository
	}
	if j.PasswordFile != "" {
		c.PasswordFile = j.PasswordFile
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
