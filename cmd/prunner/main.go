package main

import (
	"os"
	"strings"

	"github.com/apex/log"

	"github.com/Flowpack/prunner/app"
)

// These variables are set via ldflags by GoReleaser
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	err := app.New(app.Info{
		Version: version,
		Commit:  commit,
		Date:    date,
	}).Run(os.Args)
	if err != nil {
		// Do not repeat the error message for wrong flag usage
		if strings.HasPrefix(err.Error(), "flag provided but not defined") {
			os.Exit(1)
		}
		log.Fatalf("Error: %v, %T", err, err)
	}
}
