package main

import (
	"os"

	"github.com/apex/log"
	"networkteam.com/lab/prunner"
)

func main() {
	app := prunner.NewApp()

	err := app.Run(os.Args)
	if err != nil {
		log.Fatalf("Error: %s", err)
	}
}
