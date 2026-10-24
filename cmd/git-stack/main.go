package main

import (
	"fmt"
	"os"

	"gitlab.com/malephex/git-stack/internal/cli"
	"gitlab.com/malephex/git-stack/internal/exitcode"
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
