package main

import (
	"os"

	"github.com/maxie-maxie/pressurecooker/internal/cli"
)

var version = "dev"

func main() { os.Exit(cli.Main(os.Args[1:], version)) }
