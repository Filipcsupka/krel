package main

import (
	"os"

	"github.com/filipcsupka/krel/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stderr, "krel"))
}
