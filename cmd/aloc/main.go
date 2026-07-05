// Command aloc counts lines of code, comments, and blanks by language, with
// smart, language-aware exclusion of dependency and build directories.
package main

import (
	"os"

	"github.com/alyx/aloc/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
