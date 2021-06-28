package prunner

import (
	"os"

	"github.com/apex/log"
	logfmt_handler "github.com/apex/log/handlers/logfmt"
	text_handler "github.com/apex/log/handlers/text"
	"github.com/mattn/go-isatty"
	"github.com/urfave/cli/v2"
)

func NewApp() *cli.App {
	app := cli.NewApp()
	app.Usage = "Pipeline runner"

	app.Before = func(c *cli.Context) error {
		setLogHandler(c)
		return nil
	}
	app.Action = run
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

func setLogHandler(c *cli.Context) {
	if c.Bool("verbose") {
		log.SetLevel(log.DebugLevel)
	}

	if isatty.IsTerminal(os.Stdout.Fd()) && !c.Bool("disable-ansi") {
		log.SetHandler(text_handler.New(os.Stderr))
	} else {
		log.SetHandler(logfmt_handler.New(os.Stderr))
	}
}
