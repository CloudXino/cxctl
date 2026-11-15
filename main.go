// Command cxctl manages CloudXino resources from the command line.
package main

import (
	"os"

	"github.com/cloudxino/cxctl/internal/cli"
)

// version is set by GoReleaser.
var version = "dev"

func main() {
	os.Exit(cli.Main(version, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
