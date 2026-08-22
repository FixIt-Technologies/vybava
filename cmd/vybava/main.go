package main

import (
	"fmt"
	"os"

	"github.com/FixIt-Technologies/vybava/internal/cli"
)

var version = "dev"

func main() {
	app := cli.App{Version: version, Stdout: os.Stdout, Stderr: os.Stderr}
	command, err := app.Command(os.Args[0])
	if err == nil {
		command.SetArgs(os.Args[1:])
		err = command.Execute()
	}
	if message := cli.ErrorText(err); message != "" {
		fmt.Fprintln(os.Stderr, "error:", message)
	}
	os.Exit(cli.ExitCode(err))
}
