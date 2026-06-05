package main

import (
	"os"
)

func main() {
	if err := Execute(os.Args[1:]); err != nil {
		os.Exit(1)
	}
}
