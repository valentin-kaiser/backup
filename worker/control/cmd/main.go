package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/config"
	"github.com/Valentin-Kaiser/backup/pkg/manager"
	"github.com/Valentin-Kaiser/backup/pkg/runner"
	"github.com/Valentin-Kaiser/backup/pkg/store"
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

func init() {
	// Client commands never touch the daemon configuration or the go-core flags.
	if len(os.Args) > 1 && cli.Commands[os.Args[1]] {
		os.Exit(cli.Run(os.Args[1:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "daemon" {
		os.Args = append(os.Args[:1], os.Args[2:]...)
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
	m.SetUpdater(config.Update)

	config.OnChange(func(o, n *config.Config) error {
		log.Info().Msg("applying configuration changes")
		if o.LogLevel != n.LogLevel {
			if adapter, ok := logging.GetGlobalAdapter[logging.Adapter](); ok {
				adapter.SetLevel(logging.Level(n.LogLevel))
			}
		}
		return m.Reload()
	})
	config.Watch(m.Context())

	if err := m.Start(); err != nil {
		log.Fatal().Err(err).Msg("failed to start the scheduler")
	}

	if c.API.Enabled {
		if err := web.Start(api.New(m)); err != nil {
			log.Fatal().Err(err).Msg("failed to start the API server")
		}
	}

	signal := interruption.OnSignal([]func() error{
		func() error {
			log.Info().Msg("shutting down...")
			config.Stop()
			if c.API.Enabled {
				if err := web.Stop(); err != nil {
					log.Warn().Err(err).Msg("stopping the API server failed")
				}
			}
			m.Shutdown(time.Duration(config.Get().StopGraceSecs) * time.Second)
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
