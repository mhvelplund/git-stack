package main

import (
	"fmt"
	"os"

	"github.com/mhvelplund/git-stack/internal/cli"
	"github.com/mhvelplund/git-stack/internal/exitcode"
)

func main() {
	app, err := cli.NewApp()
	if err == nil {
		err = cli.Execute(app, os.Args[1:])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(exitcode.Of(err))
	}
}
