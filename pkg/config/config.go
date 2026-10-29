// Package config provides the configuration of control.
package config

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/Valentin-Kaiser/backup/pkg/notify"
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
	// TypeSync keeps two directories in sync in both directions (rclone bisync). It does not use a repository.
	TypeSync = "sync"
)

// Conflict resolution policies of sync jobs, the values of rclone's --conflict-resolve.
const (
	ConflictNewer   = "newer"
	ConflictOlder   = "older"
	ConflictLarger  = "larger"
	ConflictSmaller = "smaller"
	ConflictPath1   = "path1"
	ConflictPath2   = "path2"
	ConflictNone    = "none" // keep both versions, both are renamed
)

// Loser handling of sync jobs, the values of rclone's --conflict-loser.
const (
	LoserNumber   = "num"      // keep the loser next to the winner as file.conflict1
	LoserPathname = "pathname" // keep the loser as file.path1 or file.path2
	LoserDelete   = "delete"
)

// Sync engines. rclone bisync is the default, unison is the mature alternative.
const (
	EngineRclone = "rclone"
	EngineUnison = "unison"
)

// PeerPrefix starts the second path of a sync job that lives on another worker: peer:<name>[/subdirectory].
const PeerPrefix = "peer:"

// RestoreJob is the reserved name of restore runs, which are never scheduled.
const RestoreJob = "restore"

// cron only validates schedules, it never runs anything.
var cron = queue.NewTaskScheduler()

var jobName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// peerName is stricter than jobName, it becomes part of an rclone remote name and of environment variable names.
var peerName = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,31}$`)

// Job describes one scheduled job. In portal mode a restic job names the repository it works on, see Repository,
// and a backup job its source. Standalone mode has one implicit repository and one source for everything.
type Job struct {
	Name           string `usage:"unique name of the job, lower case letters, digits, - and _"`
	Type           string `usage:"what the job does: backup, forget or check"`
	Enabled        bool   `usage:"schedule this job"`
	Schedule       string `usage:"cron expression (minute hour day month weekday)"`
	After          string `usage:"name of a job that starts this job when it succeeds, instead of a schedule"`
	Args           string `usage:"additional restic arguments, split like a shell would"`
	TimeoutMinutes int    `usage:"abort the run after this many minutes, 0 disables the timeout"`
	MaxAgeHours    int    `usage:"health check fails when no run succeeded within this many hours, 0 disables"`
	Source         string `usage:"directory to back up, backup jobs only, required in portal mode"`
	Repository     string `usage:"name of the repository the job works on (portal mode, see repositories), restic jobs only"`

	Mounts []MountCheck `usage:"sync jobs only: volumes that must be mounted before the job runs (control.yaml only)"`

	Path1         string `usage:"sync jobs only: first directory, always local"`
	Path2         string `usage:"sync jobs only: second directory, a local directory or peer:<name>[/subdirectory] on another worker"`
	Conflict      string `usage:"sync jobs only: which version wins when a file changed on both sides (newer, older, larger, smaller, path1, path2, none = keep both), default newer"`
	ConflictLoser string `usage:"sync jobs only: what happens to the losing version (num = keep as .conflictN, pathname = keep as .path1/.path2, delete), default num"`
	MaxDeletePct  int    `usage:"sync jobs only: abort when a run would delete more than this percentage of the files, 0 uses 50"`
	CheckAccess   bool   `usage:"sync jobs only: abort unless the file RCLONE_TEST exists on both sides, protects against an unmounted volume"`
	Engine        string `usage:"sync jobs only: rclone (bisync, default) or unison"`
	Force         bool   `usage:"sync jobs only: do not abort when all files changed or too many would be deleted (rclone --force), for small directories where every file changing is normal"`
	Owner         string `usage:"sync jobs only: peer that holds the schedule and the state of this job, this worker then only mirrors it and forwards runs and changes"`
}

// Peer is another worker that exports a directory for sync jobs, see SyncServe on that worker.
type Peer struct {
	Name              string `usage:"name used in sync paths (peer:<name>), lower case letters, digits and _"`
	URL               string `usage:"WebDAV URL of the sync server of the peer, e.g. https://worker-b:9081"`
	Username          string `usage:"user name of the sync server of the peer"`
	PasswordFile      string `usage:"file with the password of the sync server of the peer"`
	PasswordSecret    string `usage:"stored secret with the password of the sync server of the peer, replaces passwordFile"`
	CAFile            string `usage:"CA file that signed the certificate of the peer, empty uses the system roots"`
	APIURL            string `usage:"URL of the control API of the peer, enables locking, mirrored jobs and starting jobs on the peer"`
	APIPasswordFile   string `usage:"file with the password of the control API of the peer"`
	APIPasswordSecret string `usage:"stored secret with the password of the control API of the peer, replaces apiPasswordFile"`
	APICAFile         string `usage:"CA file that signed the certificate of the control API of the peer"`
}

// SyncServe exports a directory of this worker to its peers, so another worker can sync with it.
type SyncServe struct {
	Enabled         bool   `usage:"serve the sync directory to peers"`
	Path            string `usage:"directory to export"`
	Address         string `usage:"address to bind to"`
	Port            int    `usage:"port to bind to"`
	Username        string `usage:"user name peers have to use"`
	PasswordFile    string `usage:"file with the password peers have to use"`
	PasswordSecret  string `usage:"stored secret with the password peers have to use, replaces passwordFile"`
	CertificateFile string `usage:"TLS certificate file, serves plain HTTP when empty"`
	KeyFile         string `usage:"TLS key file"`
	Tunnel          bool   `usage:"let peers run unison against the path through the control API (needs the API, no sshd), a peer with the password of the API can then read and write everything this worker can"`
}

// MountCheck guards against writing into an unmounted directory.
type MountCheck struct {
	Path       string `usage:"local directory that must exist before the job runs, empty disables the check"`
	MarkerFile string `usage:"file that must exist inside the path (e.g. .mounted), empty only checks the path"`
}

// DefaultRestoreRoot is the directory restores are allowed below when a repository does not name one.
const DefaultRestoreRoot = "/restore"

// DefaultRepository is the name of the one implicit repository of standalone mode.
const DefaultRepository = "default"

// Repository is a restic repository of portal mode. The jobs that work on it refer to it by name and share its
// password, its mount guard and the directory restores go to.
type Repository struct {
	Name           string     `usage:"unique name of the repository, lower case letters, digits, - and _"`
	Path           string     `usage:"restic repository (a directory or any other restic repository address)"`
	PasswordSecret string     `usage:"name of the stored secret that holds the repository password"`
	Mount          MountCheck `usage:"mount guard: the volume of the repository that must be mounted before any job works on it"`
	RestoreRoot    string     `usage:"restores of this repository are only allowed below this directory, /restore when empty"`
}

// Root returns the directory restores of the repository are limited to.
func (r Repository) Root() string {
	if r.RestoreRoot != "" {
		return r.RestoreRoot
	}
	return DefaultRestoreRoot
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
	LogLevel        int             `usage:"(-1 = trace, 0 = debug, 1 = info, 2 = warn, 3 = error)"`
	WorkerName      string          `usage:"name of this worker, the name its peers use for it (lower case letters, digits and _), defaults to the host name"`
	Mode            string          `usage:"standalone (one source, one repository, one job per type, password file, no API) or ui (management API for the portal, many jobs, stored secrets)"`
	Repositories    []Repository    `usage:"restic repositories, portal mode only (standalone mode has the one repository below)"`
	Repository      string          `usage:"standalone mode only: restic repository"`
	PasswordFile    string          `usage:"standalone mode only: file containing the repository password"`
	PasswordSecret  string          `usage:"set while a job is resolved, not a setting"`
	MasterKeyFile   string          `usage:"file with the key that encrypts the stored secrets (base64, 32 bytes), created in the data directory when empty"`
	Source          string          `usage:"standalone mode only: directory to back up"`
	Compression     string          `usage:"restic compression mode (auto, off, max)"`
	CacheDir        string          `usage:"restic cache directory"`
	Nice            int             `usage:"niceness of restic processes (0-19)"`
	ReadConcurrency int             `usage:"number of files restic reads in parallel, 0 uses the restic default"`
	PackSize        int             `usage:"target restic pack size in MiB, 0 uses the restic default"`
	OneFileSystem   bool            `usage:"do not cross filesystem boundaries while backing up"`
	LockRetry       string          `usage:"how long restic waits for a repository lock"`
	StopGraceSecs   int             `usage:"seconds a running job may finish after a shutdown signal"`
	HistoryLimit    int             `usage:"number of runs kept in the history"`
	RestoreRoot     string          `usage:"standalone mode only: restores are only allowed below this directory"`
	WebhookURL      string          `usage:"URL pinged with /start, /fail or no suffix on success (healthchecks.io compatible)"`
	Jobs            []Job           `usage:"jobs, a list of name, type, schedule, ..."`
	MountCheck      MountCheck      `usage:"standalone mode only: mount guard of the repository"`
	MountChecks     []MountCheck    `usage:"standalone mode only: additional mount guards, one entry per mounted volume (control.yaml only)"`
	SyncDir         string          `usage:"directory for the state of sync jobs (rclone bisync listings), keep it on a volume"`
	Peers           []Peer          `usage:"other workers that sync jobs can use as second path"`
	SyncServe       SyncServe       `usage:"export a directory to peers"`
	API             API             `usage:"management API used by the CLI and the web UI"`
	Notify          notify.Settings `usage:"mail notifications about job results, stale backups, security and lifecycle events"`
}

// Default returns the default configuration. The run mode is the one setting read from the environment here, because
// the defaults of the jobs and the password depend on it.
//
// Portal mode starts without repositories and jobs: create repositories (each with a stored password secret) and the
// jobs that work on them in the portal, or in control.yaml.
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
		SyncDir:         "/var/lib/control/bisync",
		SyncServe:       SyncServe{Address: "0.0.0.0", Port: 9081, Path: "/sync", Username: "sync"},
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
				Name:  "forget",
				Type:  TypeForget,
				After: "backup",
				Args:  "--prune --keep-last 7 --keep-daily 14 --keep-weekly 4 --keep-monthly 6 --keep-yearly 1",
			},
			{
				Name:           "check",
				Type:           TypeCheck,
				After:          "forget",
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
		Notify: notify.Defaults(),
	}
	if c.Portal() {
		// Everything below is per repository and per job in portal mode.
		c.Repository, c.PasswordFile, c.Source, c.RestoreRoot, c.Jobs = "", "", "", "", nil
		c.API.PasswordFile = "/run/secrets/portal_password"
	}
	return c
}

// Validate implements the go-core config interface.
func (c *Config) Validate() error {
	if c.Portal() {
		if c.Repository != "" || c.PasswordFile != "" || c.PasswordSecret != "" || c.Source != "" || c.RestoreRoot != "" ||
			c.MountCheck != (MountCheck{}) || len(c.MountChecks) > 0 {
			return apperror.NewError("repository, passwordFile, source, restoreRoot, mountCheck and mountChecks are not used in portal mode: define the repository under repositories and let the jobs name it")
		}
	} else {
		if len(c.Repositories) > 0 {
			return apperror.NewError("repositories need mode portal, standalone mode has the one repository of the repository setting")
		}
		if strings.TrimSpace(c.Repository) == "" {
			return apperror.NewError("repository must not be empty")
		}
		if strings.TrimSpace(c.Source) == "" {
			return apperror.NewError("source must not be empty")
		}
		if c.PasswordSecret != "" {
			return apperror.NewError("passwordSecret needs mode portal, standalone mode takes the password from passwordFile")
		}
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
	switch c.Mode {
	case "", ModeStandalone, ModePortal:
	default:
		return apperror.NewErrorf("mode %q must be %s or %s", c.Mode, ModeStandalone, ModePortal)
	}
	if err := c.validateRepositories(); err != nil {
		return err
	}
	if c.StopGraceSecs < 0 {
		return apperror.NewError("stopGraceSecs must not be negative")
	}
	if err := c.Notify.Validate(); err != nil {
		return err
	}
	if c.Notify.PasswordSecret != "" && !jobName.MatchString(c.Notify.PasswordSecret) {
		return apperror.NewErrorf("notify.passwordSecret %q must be 1-32 lower case letters, digits, - or _", c.Notify.PasswordSecret)
	}
	if !c.Portal() && c.Notify.PasswordSecret != "" {
		return apperror.NewError("notify.passwordSecret needs mode portal, standalone mode takes the password from notify.passwordFile")
	}
	switch c.Compression {
	case "auto", "off", "max":
	default:
		return apperror.NewErrorf("compression %q must be auto, off or max", c.Compression)
	}
	if err := c.validatePeers(); err != nil {
		return err
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
		case TypeBackup, TypeForget, TypeCheck, TypeSync:
		default:
			return apperror.NewErrorf("job %s: type %q must be backup, forget, check or sync", j.Name, j.Type)
		}
		if j.Enabled && strings.TrimSpace(j.Schedule) == "" {
			return apperror.NewErrorf("job %s: schedule must not be empty when the job is enabled", j.Name)
		}
		if strings.TrimSpace(j.Schedule) != "" {
			if err := cron.ValidateCronSpec(j.Schedule); err != nil {
				return apperror.NewErrorf("job %s: invalid schedule %q", j.Name, j.Schedule).AddError(err)
			}
		}
		if err := c.validateAfter(j); err != nil {
			return err
		}
		if j.TimeoutMinutes < 0 || j.MaxAgeHours < 0 {
			return apperror.NewErrorf("job %s: timeoutMinutes and maxAgeHours must not be negative", j.Name)
		}
		if j.Source != "" && j.Type != TypeBackup {
			return apperror.NewErrorf("job %s: source is only valid for backup jobs", j.Name)
		}
		if j.Type == TypeSync {
			// Sync jobs have no repository and no password, they only need their paths.
			if err := c.validateSync(j); err != nil {
				return err
			}
			continue
		}
		if len(j.Mounts) > 0 {
			return apperror.NewErrorf("job %s: mounts are only valid for sync jobs, the mount guard of a restic job belongs to its repository", j.Name)
		}
		if j.Owner != "" || j.Path1 != "" || j.Path2 != "" || j.Conflict != "" || j.ConflictLoser != "" || j.MaxDeletePct != 0 || j.CheckAccess || j.Force || j.Engine != "" {
			return apperror.NewErrorf("job %s: path1, path2, engine, conflict, conflictLoser, maxDeletePct, force and checkAccess are only valid for sync jobs", j.Name)
		}
		if c.Portal() {
			if _, ok := c.Repo(j.Repository); !ok {
				return apperror.NewErrorf("job %s: repository %q does not exist, set repository to one of the repositories", j.Name, j.Repository)
			}
			if j.Type == TypeBackup && !isAbs(j.Source) {
				return apperror.NewErrorf("job %s: source %q must be an absolute path", j.Name, j.Source)
			}
			continue
		}
		// Standalone: one source and one repository for everything, one job per type.
		if j.Repository != "" || j.Source != "" {
			return apperror.NewErrorf("job %s: repository and source are global in standalone mode, use mode portal for per job settings", j.Name)
		}
		if types[j.Type] {
			return apperror.NewErrorf("job %s: standalone mode allows one %s job, use mode portal for more", j.Name, j.Type)
		}
		types[j.Type] = true
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

// validateAfter checks the job that has to succeed before j starts: it must exist, be run by this worker and not lead
// back to j. A job that follows another one has no schedule of its own.
func (c *Config) validateAfter(j Job) error {
	if j.After == "" {
		return nil
	}
	if j.Enabled {
		return apperror.NewErrorf("job %s: a job that runs after %q is not scheduled, set enabled to false", j.Name, j.After)
	}
	if j.Mirror() {
		return apperror.NewErrorf("job %s: a mirrored job cannot run after another job", j.Name)
	}
	for cur, steps := j, 0; cur.After != ""; steps++ {
		prev, ok := c.Job(cur.After)
		switch {
		case !ok:
			return apperror.NewErrorf("job %s: job %q does not exist", cur.Name, cur.After)
		case prev.Mirror():
			return apperror.NewErrorf("job %s: job %q is mirrored from a peer and cannot start other jobs", cur.Name, prev.Name)
		case prev.Name == j.Name || steps > len(c.Jobs):
			return apperror.NewErrorf("job %s: runs after itself through %q", j.Name, cur.After)
		}
		cur = prev
	}
	return nil
}

// Followers returns the names of the jobs that start when the job succeeds.
func (c Config) Followers(name string) []string {
	var out []string
	for _, j := range c.Jobs {
		if j.After == name {
			out = append(out, j.Name)
		}
	}
	return out
}

// validatePeers checks the peers and the sync server.
func (c *Config) validatePeers() error {
	names := map[string]bool{}
	for i, p := range c.Peers {
		if !peerName.MatchString(p.Name) {
			return apperror.NewErrorf("peers[%d].name %q must be 1-32 lower case letters, digits or _", i, p.Name)
		}
		if names[p.Name] {
			return apperror.NewErrorf("peer name %q is used twice", p.Name)
		}
		names[p.Name] = true
		if u, err := url.Parse(p.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return apperror.NewErrorf("peer %s: url %q must be an http or https URL", p.Name, p.URL)
		}
		if p.APIURL != "" {
			if u, err := url.Parse(p.APIURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return apperror.NewErrorf("peer %s: apiUrl %q must be an http or https URL", p.Name, p.APIURL)
			}
		}
		if p.PasswordFile != "" && p.PasswordSecret != "" || p.APIPasswordFile != "" && p.APIPasswordSecret != "" {
			return apperror.NewErrorf("peer %s: set either a password file or a password secret, not both", p.Name)
		}
		for _, s := range []string{p.PasswordSecret, p.APIPasswordSecret} {
			if s != "" && !jobName.MatchString(s) {
				return apperror.NewErrorf("peer %s: secret %q must be 1-32 lower case letters, digits, - or _", p.Name, s)
			}
		}
	}
	if c.WorkerName != "" && !peerName.MatchString(c.WorkerName) {
		return apperror.NewErrorf("workerName %q must be 1-32 lower case letters, digits or _", c.WorkerName)
	}
	if c.SyncServe.PasswordFile != "" && c.SyncServe.PasswordSecret != "" {
		return apperror.NewError("set either syncServe.passwordFile or syncServe.passwordSecret, not both")
	}
	if s := c.SyncServe; s.Tunnel && !filepath.IsAbs(s.Path) {
		return apperror.NewError("syncServe.path must be an absolute path")
	}
	if s := c.SyncServe; s.Enabled {
		if !filepath.IsAbs(s.Path) {
			return apperror.NewError("syncServe.path must be an absolute path")
		}
		if s.Port < 1 || s.Port > 65535 {
			return apperror.NewError("syncServe.port must be between 1 and 65535")
		}
		if s.Username == "" || (s.PasswordFile == "" && s.PasswordSecret == "") {
			return apperror.NewError("syncServe.username and a passwordFile or passwordSecret are required: peers have to authenticate")
		}
		if (s.CertificateFile == "") != (s.KeyFile == "") {
			return apperror.NewError("syncServe.certificateFile and syncServe.keyFile have to be set together")
		}
	}
	return nil
}

// validateSync checks a sync job.
func (c *Config) validateSync(j Job) error {
	if j.Repository != "" || j.Source != "" {
		return apperror.NewErrorf("job %s: repository and source are not used by sync jobs", j.Name)
	}
	for i, mc := range j.Mounts {
		if err := validateMount(mc); err != nil {
			return apperror.NewErrorf("job %s: mounts[%d]: %v", j.Name, i, err)
		}
	}
	if !filepath.IsAbs(j.Path1) {
		return apperror.NewErrorf("job %s: path1 %q must be an absolute local path", j.Name, j.Path1)
	}
	if j.Owner != "" {
		if _, found := c.Peer(j.Owner); !found {
			return apperror.NewErrorf("job %s: owner %q is not a peer", j.Name, j.Owner)
		}
		if peer, _, ok := j.PeerPath(); !ok || peer != j.Owner {
			return apperror.NewErrorf("job %s: the path2 of a mirrored job must be peer:%s", j.Name, j.Owner)
		}
	}
	if peer, _, ok := j.PeerPath(); ok {
		if _, found := c.Peer(peer); !found {
			return apperror.NewErrorf("job %s: path2 refers to unknown peer %q", j.Name, peer)
		}
	} else {
		if !filepath.IsAbs(j.Path2) {
			return apperror.NewErrorf("job %s: path2 %q must be an absolute local path or peer:<name>", j.Name, j.Path2)
		}
		a, b := filepath.Clean(j.Path1), filepath.Clean(j.Path2)
		if a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator)) {
			return apperror.NewErrorf("job %s: path1 and path2 must not be the same or inside each other", j.Name)
		}
	}
	switch j.Engine {
	case "", EngineRclone, EngineUnison:
	default:
		return apperror.NewErrorf("job %s: engine %q must be rclone or unison", j.Name, j.Engine)
	}
	if j.Unison() {
		if err := c.validateUnison(j); err != nil {
			return err
		}
	}
	switch j.Conflict {
	case "", ConflictNewer, ConflictOlder, ConflictLarger, ConflictSmaller, ConflictPath1, ConflictPath2, ConflictNone:
	default:
		return apperror.NewErrorf("job %s: conflict %q must be newer, older, larger, smaller, path1, path2 or none", j.Name, j.Conflict)
	}
	switch j.ConflictLoser {
	case "", LoserNumber, LoserPathname, LoserDelete:
	default:
		return apperror.NewErrorf("job %s: conflictLoser %q must be num, pathname or delete", j.Name, j.ConflictLoser)
	}
	if j.MaxDeletePct < 0 || j.MaxDeletePct > 100 {
		return apperror.NewErrorf("job %s: maxDeletePct must be between 0 and 100", j.Name)
	}
	return nil
}

// validateUnison checks what only matters to the unison engine: it has fewer policies than rclone and talks to a peer
// through the control API.
func (c *Config) validateUnison(j Job) error {
	switch j.Conflict {
	case "", ConflictNewer, ConflictOlder, ConflictPath1, ConflictPath2, ConflictNone:
	default:
		return apperror.NewErrorf("job %s: unison resolves conflicts with newer, older, path1, path2 or none (skip and report), not %q", j.Name, j.Conflict)
	}
	if j.MaxDeletePct != 0 || j.Force {
		return apperror.NewErrorf("job %s: maxDeletePct and force belong to rclone, unison refuses to empty a replica on its own", j.Name)
	}
	if peer, _, ok := j.PeerPath(); ok {
		if p, found := c.Peer(peer); found && p.APIURL == "" {
			return apperror.NewErrorf("job %s: unison reaches peer %s through its control API, set the apiUrl of the peer", j.Name, peer)
		}
	}
	return nil
}

// Unison reports whether the job syncs with unison.
func (j Job) Unison() bool { return j.Engine == EngineUnison }

// PeerPath splits the second path of a sync job that lives on a peer into the peer name and the subdirectory.
func (j Job) PeerPath() (peer, dir string, ok bool) {
	rest, ok := strings.CutPrefix(j.Path2, PeerPrefix)
	if !ok {
		return "", "", false
	}
	peer, dir, _ = strings.Cut(rest, "/")
	return peer, strings.Trim(dir, "/"), true
}

// Mirror reports whether this worker only mirrors the job: a peer owns its schedule and state.
func (j Job) Mirror() bool { return j.Owner != "" }

// Worker returns the name of this worker, the host name when none is configured.
func (c Config) Worker() string {
	if c.WorkerName != "" {
		return c.WorkerName
	}
	h, _ := os.Hostname()
	h = strings.ToLower(h)
	var b strings.Builder
	for _, r := range h {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	if n := b.String(); peerName.MatchString(n) {
		return n
	}
	return "worker"
}

// Peer returns the peer with the given name.
func (c Config) Peer(name string) (Peer, bool) {
	for _, p := range c.Peers {
		if p.Name == name {
			return p, true
		}
	}
	return Peer{}, false
}

// SyncKey identifies the pair of directories of a sync job, jobs with the same pair never run at the same time.
func SyncKey(j Job) string {
	a, b := j.Path1, j.Path2
	if b < a {
		a, b = b, a
	}
	return "sync:" + a + "|" + b
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

// validateMount checks a mount guard.
func validateMount(mc MountCheck) error {
	if mc.Path == "" {
		if mc.MarkerFile != "" {
			return apperror.NewError("a marker file needs a path")
		}
		return nil
	}
	if !isAbs(mc.Path) {
		return apperror.NewErrorf("path %q must be absolute", mc.Path)
	}
	return nil
}

// validateRepositories checks the repositories of portal mode.
func (c *Config) validateRepositories() error {
	names, paths := map[string]bool{}, map[string]string{}
	for i, r := range c.Repositories {
		if !jobName.MatchString(r.Name) {
			return apperror.NewErrorf("repositories[%d].name %q must be 1-32 lower case letters, digits, - or _", i, r.Name)
		}
		if names[r.Name] {
			return apperror.NewErrorf("repository name %q is used twice", r.Name)
		}
		names[r.Name] = true
		if strings.TrimSpace(r.Path) == "" {
			return apperror.NewErrorf("repository %s: path must not be empty", r.Name)
		}
		// Two names for one repository would defeat the one-job-at-a-time lock of the repository.
		if other, ok := paths[r.Path]; ok {
			return apperror.NewErrorf("repository %s: path %q is already used by repository %s", r.Name, r.Path, other)
		}
		paths[r.Path] = r.Name
		if !jobName.MatchString(r.PasswordSecret) {
			return apperror.NewErrorf("repository %s: passwordSecret %q must be 1-32 lower case letters, digits, - or _", r.Name, r.PasswordSecret)
		}
		if err := validateMount(r.Mount); err != nil {
			return apperror.NewErrorf("repository %s: mount: %v", r.Name, err)
		}
		if r.RestoreRoot != "" && !isAbs(r.RestoreRoot) {
			return apperror.NewErrorf("repository %s: restoreRoot %q must be an absolute path", r.Name, r.RestoreRoot)
		}
	}
	return nil
}

// Repo returns the repository with the given name. Standalone mode has one implicit repository, DefaultRepository.
func (c Config) Repo(name string) (Repository, bool) {
	if !c.Portal() {
		if name == "" || name == DefaultRepository {
			return c.implicit(), true
		}
		return Repository{}, false
	}
	for _, r := range c.Repositories {
		if r.Name == name {
			return r, true
		}
	}
	return Repository{}, false
}

// Repos returns the repositories: the configured ones in portal mode, the implicit one in standalone mode.
func (c Config) Repos() []Repository {
	if !c.Portal() {
		return []Repository{c.implicit()}
	}
	return c.Repositories
}

// implicit is the one repository of standalone mode, described by the top level settings.
func (c Config) implicit() Repository {
	return Repository{Name: DefaultRepository, Path: c.Repository, Mount: c.MountCheck, RestoreRoot: c.RestoreRoot}
}

// JobsOf returns the names of the restic jobs that work on the repository.
func (c Config) JobsOf(repository string) []string {
	var out []string
	for _, j := range c.Jobs {
		if j.Type == TypeSync {
			continue
		}
		if r, ok := c.Repo(j.Repository); ok && r.Name == repository {
			out = append(out, j.Name)
		}
	}
	return out
}

// UsedByJobs reports whether a restic job works on the repository.
func (c Config) UsedByJobs(repository string) bool { return len(c.JobsOf(repository)) > 0 }

// ForRepository returns the configuration with the repository applied. In standalone mode that is the
// configuration itself.
func (c Config) ForRepository(r Repository) Config {
	if !c.Portal() {
		return c
	}
	c.Repository, c.PasswordFile, c.PasswordSecret = r.Path, "", r.PasswordSecret
	c.MountCheck, c.MountChecks = r.Mount, nil
	c.RestoreRoot = r.Root()
	return c
}

// ForJob returns the configuration with the repository, password, source and mount guards of the job applied. A
// restic job gets the repository it names, a sync job its own mounts.
func (c Config) ForJob(j Job) Config {
	if j.Type == TypeSync {
		c.MountCheck, c.MountChecks = MountCheck{}, j.Mounts
		return c
	}
	if r, ok := c.Repo(j.Repository); ok {
		c = c.ForRepository(r)
	}
	if j.Source != "" {
		c.Source = j.Source
	}
	return c
}

// MountGuards returns every mount guard that has to pass before a job runs, the global one first and then the
// additional ones, without the entries that check nothing.
func (c Config) MountGuards() []MountCheck {
	guards := make([]MountCheck, 0, len(c.MountChecks)+1)
	if c.MountCheck.Path != "" {
		guards = append(guards, c.MountCheck)
	}
	for _, mc := range c.MountChecks {
		if mc.Path != "" {
			guards = append(guards, mc)
		}
	}
	return guards
}

// RepositoryKey identifies the repository a job works on, jobs with the same key never run at the same time.
func (c Config) RepositoryKey(j Job) string {
	if j.Type == TypeSync {
		return SyncKey(j)
	}
	return c.ForJob(j).Repository
}

// mode is the run mode from CONTROL_MODE, standalone when it is not set.
func mode() string {
	if v := strings.TrimSpace(os.Getenv("CONTROL_MODE")); v != "" {
		return v
	}
	return ModeStandalone
}

// isAbs reports whether p is an absolute path. The paths of a configuration are paths inside the container, so a
// leading slash counts on every platform.
func isAbs(p string) bool { return filepath.IsAbs(p) || strings.HasPrefix(p, "/") }
