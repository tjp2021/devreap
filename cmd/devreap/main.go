package main

import (
	"os"

	"github.com/tjp2021/devreap/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
