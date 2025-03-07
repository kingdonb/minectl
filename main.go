package main

import (
	"fmt"
	"os"

	"github.com/minectl/cmd/minectl"
)

var (
	version string
	commit  string
)

func main() {
	if err := minectl.Execute(version, commit); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err.Error())
		os.Exit(1)
	}
}
