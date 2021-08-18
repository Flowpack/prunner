package app

import (
	"github.com/Flowpack/prunner/store"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"

	"github.com/apex/log"
	logfmt_handler "github.com/apex/log/handlers/logfmt"
	text_handler "github.com/apex/log/handlers/text"
	"github.com/friendsofgo/errors"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/jwtauth/v5"
	"github.com/mattn/go-isatty"
	"github.com/urfave/cli/v2"

	"github.com/Flowpack/prunner"
	"github.com/Flowpack/prunner/config"
	"github.com/Flowpack/prunner/definition"
	"github.com/Flowpack/prunner/server"
	"github.com/Flowpack/prunner/taskctl"
)

// New builds a CLI app with the main entry point (called from cmd/prunner/main.go)
func New() *cli.App {
	app := cli.NewApp()
	app.Usage = "Pipeline runner"

	app.Before = func(c *cli.Context) error {
		setLogHandler(c)
		return nil
	}
	// this is the main action - see prunner.go
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
			Name:    "disable-ansi",
			Usage:   "Force disable ANSI log output and output log in logfmt format",
			EnvVars: []string{"PRUNNER_DISABLE_ANSI"},
			Value:   false,
		},
		&cli.StringFlag{
			Name:    "config",
			Usage:   "Config filename (will be created on first run)",
			Value:   ".prunner.yml",
			EnvVars: []string{"PRUNNER_CONFIG"},
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
	}

	app.Commands = []*cli.Command{
		newDebugCmd(),
	}

	return app
}

// appAction is the main function which starts everything including the HTTP server.
func appAction(c *cli.Context) error {
	conf, err := config.LoadOrCreateConfig(c.String("config"))
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
		WithField("component", "cli").
		WithField("pipelines", defs.Pipelines.NamesWithSourcePath()).
		Infof("Loaded %d pipeline definitions", len(defs.Pipelines))

	// TODO Handle signal USR1 for reloading config

	outputStore, err := taskctl.NewOutputStore(path.Join(c.String("data"), "logs"))
	if err != nil {
		return errors.Wrap(err, "building output store")
	}

	store, err := store.NewJSONDataStore(path.Join(c.String("data")))
	if err != nil {
		return errors.Wrap(err, "building pipeline runner store")
	}

	// Set up pipeline runner
	pRunner, err := prunner.NewPipelineRunner(c.Context, defs, func() taskctl.Runner {
		// taskctl.NewTaskRunner never actually returns an error
		taskRunner, _ := taskctl.NewTaskRunner(outputStore)

		// Do not output task stdout / stderr to the server process. NOTE: Before/After execution logs won't be visible because of this
		taskRunner.Stdout = io.Discard
		taskRunner.Stderr = io.Discard

		return taskRunner
	}, store)
	if err != nil {
		return err
	}

	srv := server.NewServer(
		pRunner,
		outputStore,
		middleware.RequestLogger(createLogFormatter(c)),
		tokenAuth,
	)

	// Set up a simple REST API for listing jobs and scheduling pipelines

	log.
		WithField("component", "cli").
		Infof("HTTP API Listening on %s", c.String("address"))
	return http.ListenAndServe(c.String("address"), srv)
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
