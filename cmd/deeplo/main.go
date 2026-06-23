package main

import (
	"fmt"
	"os"

	"github.com/jancernik/deeplo/internal/cli"
)

func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		if !cli.IsSilentExit(err) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}
