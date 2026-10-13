// Command control-portal serves the web portal of the control daemon. It is a separate process, the daemon runs without it.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/auth"
	"github.com/Valentin-Kaiser/backup/worker/portal/pkg/audit"
	"github.com/Valentin-Kaiser/backup/worker/portal/pkg/config"
	"github.com/Valentin-Kaiser/backup/worker/portal/pkg/login"
	"github.com/Valentin-Kaiser/backup/worker/portal/pkg/web"
	"github.com/valentin-kaiser/go-core/apperror"
	"github.com/valentin-kaiser/go-core/flag"
	"github.com/valentin-kaiser/go-core/interruption"
	"github.com/valentin-kaiser/go-core/logging"
	"github.com/valentin-kaiser/go-core/logging/log"
	"github.com/valentin-kaiser/go-core/version"
)

func init() {
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
	config.OnChange(func(o, n *config.Config) error {
		log.Info().Msg("applying configuration changes")
		if o.LogLevel != n.LogLevel {
			if adapter, ok := logging.GetGlobalAdapter[logging.Adapter](); ok {
				adapter.SetLevel(logging.Level(n.LogLevel))
			}
		}
		if o.Address != n.Address || o.Port != n.Port || o.TLS != n.TLS || o.CertificateFile != n.CertificateFile ||
			o.KeyFile != n.KeyFile || o.ClientCAFile != n.ClientCAFile {
			log.Info().Msg("restarting the web server due to an address, port or TLS change")
			go web.Restart()
		}
		return nil
	})
}

func main() {
	defer interruption.Catch()

	apperror.WithDetails = flag.Debug
	logging.Debug(flag.Debug)
	logging.SetGlobalAdapter(logging.
		NewZerologAdapter().
		WithConsole().
		WithFileRotation(filepath.Join(flag.Path, "logs", "portal.log"), 10, 30, 5, true).
		SetLevel(logging.Level(config.Get().LogLevel)))

	if flag.Help {
		flag.PrintHelp()
		return
	}
	if flag.Version {
		fmt.Print(version.String())
		return
	}

	log.Info().Msgf("=== backup control portal %s ===", version.String())

	rootCtx, cancel := context.WithCancel(context.Background())
	config.Watch(rootCtx)

	if created, err := auth.Ensure(config.Get().PasswordFile); err != nil {
		log.Warn().Err(err).Msg("the portal password could not be created")
	} else if created {
		log.Info().Msgf("no password was set, a random one was created in %s", config.Get().PasswordFile)
	}
	password := auth.New(func() string { return config.Get().PasswordFile })
	if _, err := password.Value(); err != nil {
		log.Warn().Err(err).Msg("the portal has no usable password and nobody can sign in until the password file is fixed")
	}
	store, err := login.Open(flag.Path, password)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to open the portal database")
	}

	aud, err := audit.New(store.DB(), time.Duration(config.Get().AuditRetentionDays)*24*time.Hour)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to open the audit log")
	}
	go aud.Run(rootCtx)

	if err := web.Start(store, password, aud); err != nil {
		log.Fatal().Err(err).Msg("failed to start the web server")
	}

	signal := interruption.OnSignal([]func() error{
		func() error {
			log.Info().Msg("shutting down...")
			config.Stop()
			cancel()
			_ = store.Close()
			if err := web.Stop(); err != nil {
				log.Warn().Err(err).Msg("stopping the web server failed")
			}
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
