package main

import (
	"os"

	"github.com/zacharytgray/yonder/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:]))
}
