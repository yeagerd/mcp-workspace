package main

import (
	"os"

	"github.com/articulant/tmux-harness/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
