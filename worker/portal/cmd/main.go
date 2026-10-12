// Command control-portal serves the web portal of the control daemon. It is a separate process, the daemon runs without it.
package main

import (
	"context"
	"fmt"
	"os"
	"syscall"

	"github.com/Valentin-Kaiser/backup/worker/portal/pkg/config"
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
}

func main() {
	defer interruption.Catch()

	apperror.WithDetails = flag.Debug
	logging.Debug(flag.Debug)
	logging.SetGlobalAdapter(logging.
		NewZerologAdapter().
		WithConsole().
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

	if err := web.Start(); err != nil {
		log.Fatal().Err(err).Msg("failed to start the web server")
	}

	signal := interruption.OnSignal([]func() error{
		func() error {
			log.Info().Msg("shutting down...")
			config.Stop()
			cancel()
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
