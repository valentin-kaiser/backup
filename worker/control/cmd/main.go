package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/auth"
	"github.com/Valentin-Kaiser/backup/pkg/config"
	"github.com/Valentin-Kaiser/backup/pkg/manager"
	"github.com/Valentin-Kaiser/backup/pkg/notify"
	"github.com/Valentin-Kaiser/backup/pkg/runner"
	"github.com/Valentin-Kaiser/backup/pkg/secrets"
	"github.com/Valentin-Kaiser/backup/pkg/secure"
	"github.com/Valentin-Kaiser/backup/pkg/store"
	"github.com/Valentin-Kaiser/backup/pkg/syncserve"
	"github.com/Valentin-Kaiser/backup/pkg/tunnel"
	"github.com/Valentin-Kaiser/backup/worker/control/pkg/bootstrap"
	"github.com/Valentin-Kaiser/backup/worker/control/pkg/cli"
	"github.com/Valentin-Kaiser/backup/worker/control/pkg/web"
	api "github.com/Valentin-Kaiser/backup/worker/control/pkg/web/api/v1"
	"github.com/valentin-kaiser/go-core/apperror"
	"github.com/valentin-kaiser/go-core/flag"
	"github.com/valentin-kaiser/go-core/interruption"
	"github.com/valentin-kaiser/go-core/logging"
	"github.com/valentin-kaiser/go-core/logging/log"
	"github.com/valentin-kaiser/go-core/version"
)

// once is the job type of the "once" command, empty when the daemon runs.
var once string

func init() {
	// unison starts this program as its "ssh" (a link to control): relay its protocol to the peer, nothing else.
	if filepath.Base(os.Args[0]) == tunnel.ClientName || filepath.Base(os.Args[0]) == tunnel.ClientName+".exe" {
		os.Exit(tunnel.RunClient(os.Args[1:]))
	}
	// Client commands never touch the daemon configuration or the go-core flags.
	if len(os.Args) > 1 && cli.Commands[os.Args[1]] {
		os.Exit(cli.Run(os.Args[1:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "daemon" {
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}
	// "once <type>" runs the one job of that type and exits, it needs the configuration but no daemon.
	if len(os.Args) > 2 && os.Args[1] == "once" {
		once = os.Args[2]
		os.Args = append(os.Args[:1], os.Args[3:]...)
	}

	defer interruption.Catch()
	interruption.Write = true

	logging.Anonymous(true)
	apperror.Anonymous(true)
	apperror.ErrorHandler = func(err error, msg string) {
		log.Error().Err(err).Msg(msg)
	}
	logging.SetGlobalAdapter(logging.
		NewZerologAdapter().
		WithConsole().
		SetLevel(logging.InfoLevel))

	config.Init()
}

func main() {
	defer interruption.Catch()

	apperror.WithDetails = flag.Debug
	logging.Debug(flag.Debug)
	c := config.Get()
	logging.SetGlobalAdapter(logging.
		NewZerologAdapter().
		WithConsole().
		WithFileRotation(filepath.Join(flag.Path, "logs", "control.log"), 10, 30, 5, true).
		SetLevel(logging.Level(c.LogLevel)))

	if flag.Help {
		flag.PrintHelp()
		return
	}
	if flag.Version {
		fmt.Print(version.String())
		return
	}

	log.Info().Msgf("=== backup control %s ===", version.String())

	st, err := store.Open(filepath.Join(flag.Path, "history.jsonl"), c.HistoryLimit)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to open the run history")
	}
	m := manager.New(config.Get, runner.Runner{}, st)

	// Mail notifications read the SMTP password from a file, or from the stored secrets in portal mode.
	var secretStore *secrets.Store
	notify.Configure(func() notify.Settings { return config.Get().Notify }, func(secret, file string) (string, error) {
		if secret != "" && secretStore != nil {
			return secretStore.Get(secret)
		}
		return notify.FileResolver(secret, file)
	})

	if once != "" {
		if err := notify.Start(); err != nil {
			log.Warn().Err(err).Msg("mail notifications are not available")
		}
		code := bootstrap.RunOnce(m, once)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		notify.Stop(ctx)
		cancel()
		os.Exit(code)
	}

	// Standalone mode only runs the jobs of the configuration on their schedules: no API, no secret store and no
	// editing of the jobs. portal mode adds them.
	if c.Portal() {
		m.SetUpdater(config.Update)

		key, err := secure.LoadOrCreateKey(bootstrap.MasterKeyPath(c))
		if err != nil {
			log.Fatal().Err(err).Msg("failed to load the master key")
		}
		secretStore, err = secrets.Open(flag.Path, key)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to open the stored secrets")
		}
		m.SetSecrets(secretStore)

		config.OnChange(func(o, n *config.Config) error {
			log.Info().Msg("applying configuration changes")
			if o.LogLevel != n.LogLevel {
				if adapter, ok := logging.GetGlobalAdapter[logging.Adapter](); ok {
					adapter.SetLevel(logging.Level(n.LogLevel))
				}
			}
			if c.API.Enabled && (o.API.Address != n.API.Address || o.API.Port != n.API.Port || o.API.TLS != n.API.TLS ||
				o.API.CertificateFile != n.API.CertificateFile || o.API.KeyFile != n.API.KeyFile ||
				o.API.ClientCAFile != n.API.ClientCAFile || o.API.RequireClientCert != n.API.RequireClientCert) {
				log.Info().Msg("restarting the API server due to an address, port or TLS change")
				go web.Restart()
			}
			if o.Notify.Mail != n.Notify.Mail || o.Notify.Enabled != n.Notify.Enabled ||
				o.Notify.PasswordFile != n.Notify.PasswordFile || o.Notify.PasswordSecret != n.Notify.PasswordSecret {
				go notify.Restart(m.Context())
			}
			return m.Reload()
		})
		config.Watch(m.Context())
	}

	if err := notify.Start(); err != nil {
		log.Warn().Err(err).Msg("mail notifications are not available")
	}
	if err := m.Start(); err != nil {
		log.Fatal().Err(err).Msg("failed to start the scheduler")
	}
	notify.Send(notify.Event{
		Kind: notify.KindLifecycle, Severity: notify.SeverityInfo, Title: "backup control started", Key: "start",
		Fields: []notify.Field{{Label: "Version", Value: version.String()}, {Label: "Mode", Value: c.Mode}},
	})

	// Export a directory to the peers of this worker. In portal mode the server follows configuration changes.
	syncServer := syncserve.Server{Password: m.SyncServePassword}
	syncServer.Start(m.Context(), c.SyncServe)
	defer syncServer.Stop()
	if c.Portal() {
		config.OnChange(func(o, n *config.Config) error {
			if o.SyncServe != n.SyncServe {
				syncServer.Start(m.Context(), n.SyncServe)
			}
			return nil
		})
	}

	if c.Portal() && c.API.Enabled {
		if !c.API.DisableAuth {
			if created, err := auth.Ensure(c.API.PasswordFile); err != nil {
				log.Warn().Err(err).Msg("the API password could not be created")
			} else if created {
				log.Info().Msgf("no password was set, a random one was created in %s", c.API.PasswordFile)
			}
		}
		password := auth.New(func() string { return config.Get().API.PasswordFile })
		if _, err := password.Value(); err != nil && !c.API.DisableAuth {
			log.Warn().Err(err).Msg("the API has no usable password and refuses every call until the password file is fixed")
		}
		if err := web.Start(api.New(m).WithSecrets(secretStore), password); err != nil {
			log.Fatal().Err(err).Msg("failed to start the API server")
		}
	} else if c.API.Enabled {
		if err := web.StartHealth(api.New(m)); err != nil {
			log.Fatal().Err(err).Msg("failed to start the health probe")
		}
	}

	signal := interruption.OnSignal([]func() error{
		func() error {
			log.Info().Msg("shutting down...")
			notify.Send(notify.Event{Kind: notify.KindLifecycle, Severity: notify.SeverityInfo, Title: "backup control stopping", Key: "stop"})
			if c.Portal() {
				config.Stop()
			}
			if c.API.Enabled {
				if err := web.Stop(); err != nil {
					log.Warn().Err(err).Msg("stopping the API server failed")
				}
			}
			m.Shutdown(time.Duration(config.Get().StopGraceSecs) * time.Second)
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
			notify.Stop(stopCtx)
			stopCancel()
			if adapter, ok := logging.GetGlobalAdapter[*logging.ZerologAdapter](); ok {
				adapter.Stop()
			}
			return nil
		},
		func() error {
			log.Info().Msg("stopped gracefully")
			return nil
		},
	}, os.Interrupt, syscall.SIGTERM)
	defer interruption.WaitForShutdown(signal)
}
