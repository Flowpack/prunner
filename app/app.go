package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"syscall"
	"time"

	"github.com/apex/log"
	logfmt_handler "github.com/apex/log/handlers/logfmt"
	text_handler "github.com/apex/log/handlers/text"
	"github.com/friendsofgo/errors"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/jwtauth/v5"
	"github.com/joho/godotenv"
	"github.com/mattn/go-isatty"
	"github.com/taskctl/taskctl/pkg/variables"
	"github.com/urfave/cli/v2"

	"github.com/Flowpack/prunner"
	"github.com/Flowpack/prunner/config"
	"github.com/Flowpack/prunner/definition"
	"github.com/Flowpack/prunner/server"
	"github.com/Flowpack/prunner/store"
	"github.com/Flowpack/prunner/taskctl"
)

// New builds a CLI app with the main entry point (called from cmd/prunner/main.go)
func New(info Info) *cli.App {
	app := cli.NewApp()
	app.Usage = "Pipeline runner"

	app.Before = func(c *cli.Context) error {
		setLogHandler(c)
		log.
			WithField("version", info.Version).
			Debug("Starting Prunner")
		err := loadDotenv(c)
		if err != nil {
			return err
		}

		return nil
	}
	app.Action = appAction
	app.Flags = []cli.Flag{
		&cli.BoolFlag{
			Name:    "verbose",
			Aliases: []string{"v"},
			Usage:   "Enable verbose log output",
			Value:   false,
			EnvVars: []string{"PRUNNER_VERBOSE"},
		},
		&cli.BoolFlag{
			Name:    "enable-profiling",
			Usage:   "Enable the Profiling endpoints underneath /debug/pprof",
			Value:   false,
			EnvVars: []string{"PRUNNER_ENABLE_PROFILING"},
		},
		&cli.BoolFlag{
			Name:    "disable-ansi",
			Usage:   "Force disable ANSI log output and output log in logfmt format",
			EnvVars: []string{"PRUNNER_DISABLE_ANSI"},
			Value:   false,
		},
		&cli.StringFlag{
			Name:    "config",
			Usage:   "Dynamic config filename (will be created on first run if jwt-secret is not set)",
			Value:   ".prunner.yml",
			EnvVars: []string{"PRUNNER_CONFIG"},
		},
		&cli.StringFlag{
			Name:    "jwt-secret",
			Usage:   "Pre-generated shared secret for JWT authentication (at least 16 characters)",
			EnvVars: []string{"PRUNNER_JWT_SECRET"},
		},
		&cli.StringFlag{
			Name:    "data",
			Usage:   "Base directory to use for storing data (metadata and job outputs)",
			Value:   ".prunner",
			EnvVars: []string{"PRUNNER_DATA"},
		},
		&cli.StringFlag{
			Name:    "pattern",
			Usage:   "Search pattern (glob) for pipeline configuration scan",
			Value:   "**/pipelines.{yml,yaml}",
			EnvVars: []string{"PRUNNER_PATTERN"},
		},
		&cli.StringFlag{
			Name:    "path",
			Usage:   "Base directory to use for pipeline configuration scan",
			Value:   ".",
			EnvVars: []string{"PRUNNER_PATH"},
		},
		&cli.StringFlag{
			Name:    "address",
			Usage:   "Listen address for HTTP API",
			Value:   "localhost:9009",
			EnvVars: []string{"PRUNNER_ADDRESS"},
		},
		&cli.StringSliceFlag{
			Name:    "env-files",
			Usage:   "Filenames with environment variables to load (dotenv style), will override existing env vars, set empty to skip loading",
			Value:   cli.NewStringSlice(".env", ".env.local"),
			EnvVars: []string{"PRUNNER_ENV_FILES"},
		},
		&cli.BoolFlag{
			Name:    "watch",
			Usage:   "Watch for pipeline configuration changes and reload them",
			EnvVars: []string{"PRUNNER_WATCH"},
		},
		&cli.DurationFlag{
			Name:    "poll-interval",
			Usage:   "Poll interval for pipeline configuration changes (if watch is enabled)",
			Value:   30 * time.Second,
			EnvVars: []string{"PRUNNER_POLL_INTERVAL"},
		},
	}

	app.Commands = []*cli.Command{
		newDebugCmd(),
		{
			Name:  "version",
			Usage: "Print the current version",
			Action: func(c *cli.Context) error {
				fmt.Println(info.Version)
				return nil
			},
		},
	}

	return app
}

func loadDotenv(c *cli.Context) error {
	if envFiles := c.StringSlice("env-files"); len(envFiles) > 0 && envFiles[0] != "" {
		for _, envFile := range envFiles {
			err := loadEnvFile(envFile)
			if err != nil {
				return errors.Wrapf(err, "loading env file %s", envFile)
			}
		}
	}
	return nil
}

func loadEnvFile(file string) error {
	f, err := os.Open(file)
	if os.IsNotExist(err) {
		// Ignore not existing dotenv files
		return nil
	} else if err != nil {
		return err
	}
	defer f.Close()

	log.
		Debugf("Loading env from %q", file)

	envVars, err := godotenv.Parse(f)
	if err != nil {
		return err
	}
	for key, value := range envVars {
		_ = os.Setenv(key, value)
	}
	return nil
}

// appAction is the main function which starts everything including the HTTP server.
func appAction(c *cli.Context) error {
	conf, err := loadConfig(c)
	if err != nil {
		return err
	}

	tokenAuth := jwtauth.New("HS256", []byte(conf.JWTSecret), nil)

	// Load declared pipelines recursively

	defs, err := definition.LoadRecursively(filepath.Join(c.String("path"), c.String("pattern")))
	if err != nil {
		return errors.Wrap(err, "loading definitions")
	}

	log.
		WithField("pipelines", defs.Pipelines.NamesWithSourcePath()).
		Infof("Loaded %d pipeline definitions", len(defs.Pipelines))

	// TODO Handle signal USR1 for reloading config

	outputStore, err := taskctl.NewOutputStore(path.Join(c.String("data"), "logs"))
	if err != nil {
		return errors.Wrap(err, "building output store")
	}

	dataStore, err := store.NewJSONDataStore(path.Join(c.String("data")))
	if err != nil {
		return errors.Wrap(err, "building pipeline runner store")
	}

	// How signals are handled:
	// - SIGINT: Shutdown gracefully and wait for jobs to be finished completely
	// - SIGTERM: Cancel running jobs

	gracefulShutdownCtx, gracefulCancel := signal.NotifyContext(c.Context, syscall.SIGINT, syscall.SIGTERM)
	defer gracefulCancel()
	// Cancel the shutdown context on SIGTERM to prevent waiting for jobs and client connections
	forcedShutdownCtx, forcedCancel := signal.NotifyContext(c.Context, syscall.SIGTERM)
	defer forcedCancel()

	// Set up pipeline runner
	pRunner, err := prunner.NewPipelineRunner(gracefulShutdownCtx, defs, func(j *prunner.PipelineJob) taskctl.Runner {
		// taskctl.NewTaskRunner never actually returns an error
		taskRunner, _ := taskctl.NewTaskRunner(outputStore, taskctl.WithEnv(variables.FromMap(j.Env)))

		// Do not output task stdout / stderr to the server process. NOTE: Before/After execution logs won't be visible because of this
		taskRunner.Stdout = io.Discard
		taskRunner.Stderr = io.Discard

		return taskRunner
	}, dataStore, outputStore)
	if err != nil {
		return err
	}

	handleDefinitionChanges(gracefulShutdownCtx, c, pRunner, defs)

	srv := server.NewServer(
		pRunner,
		outputStore,
		middleware.RequestLogger(createLogFormatter(c)),
		tokenAuth,
		c.Bool("enable-profiling"),
	)

	// Set up a simple REST API for listing jobs and scheduling pipelines
	address := c.String("address")
	log.
		Infof("HTTP API listening on %s", address)

	// Start http server and handle graceful shutdown
	httpSrv := http.Server{
		Addr:    address,
		Handler: srv,
	}
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Error starting HTTP API: %s", err)
		}
	}()

	// Wait for SIGINT or SIGTERM
	<-gracefulShutdownCtx.Done()

	log.Info("Received signal, waiting until jobs are finished...")
	_ = pRunner.Shutdown(forcedShutdownCtx)

	log.Debugf("Shutting down HTTP API...")
	_ = httpSrv.Shutdown(forcedShutdownCtx)

	log.Info("Shutdown complete")

	return nil
}

func handleDefinitionChanges(ctx context.Context, c *cli.Context, pRunner *prunner.PipelineRunner, defs *definition.PipelinesDef) {
	reloadDefinitions := func() {
		newDefs, err := definition.LoadRecursively(filepath.Join(c.String("path"), c.String("pattern")))
		if err != nil {
			log.Errorf("Error loading pipeline definitions: %s", err)
			return
		}

		if defs.Equals(*newDefs) {
			log.Debug("Reloading pipeline definitions: no changes detected")
			return
		}
		defs = newDefs
		pRunner.ReplaceDefinitions(defs)
		log.
			WithField("pipelines", defs.Pipelines.NamesWithSourcePath()).
			Infof("Definitions changed, loaded %d pipelines", len(defs.Pipelines))
	}

	watchEnabled := c.Bool("watch")
	go func() {
		// We always start a ticker and defer the decision whether to actually reloading depending on watchEnabled.
		// This makes it easier to use a single select statement.
		pollInterval := c.Duration("poll-interval")
		t := time.NewTicker(pollInterval)
		defer t.Stop()

		notifyReload := make(chan os.Signal, 1)
		defer close(notifyReload)
		notifyReloadSignal(notifyReload)

		for {
			select {
			case <-t.C:
				if watchEnabled {
					reloadDefinitions()
				}
			case <-notifyReload:
				log.Info("Received SIGUSR1, reloading pipeline definitions")
				reloadDefinitions()
			case <-ctx.Done():
				return
			}
		}
	}()
}

func loadConfig(c *cli.Context) (*config.Config, error) {
	conf, err := config.LoadOrCreateConfig(
		c.String("config"),
		config.Config{JWTSecret: c.String("jwt-secret")},
	)
	return conf, err
}

func createLogFormatter(c *cli.Context) middleware.LogFormatter {
	if useAnsiOutput(c) {
		return server.DevelopmentLogFormatter(log.Log)
	} else {
		return server.StructuredLogFormatter(log.Log)
	}
}

func setLogHandler(c *cli.Context) {
	if c.Bool("verbose") {
		log.SetLevel(log.DebugLevel)
	}

	if useAnsiOutput(c) {
		log.SetHandler(text_handler.New(os.Stderr))
	} else {
		log.SetHandler(logfmt_handler.New(os.Stderr))
	}
}

func useAnsiOutput(c *cli.Context) bool {
	return isatty.IsTerminal(os.Stdout.Fd()) && !c.Bool("disable-ansi")
}
