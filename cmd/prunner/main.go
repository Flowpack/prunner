package main

import (
	"os"

	"github.com/apex/log"

	"github.com/Flowpack/prunner/app"
)

func main() {
	err := app.New().Run(os.Args)
	if err != nil {
		log.Fatalf("Error: %s", err)
	}
}
