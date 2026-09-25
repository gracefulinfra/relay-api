// Command relay-api is a bootstrap smoke binary: it prints its build metadata and exits.
// Product code replaces it in a later prompt.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/gracefulinfra/relay-api/internal/buildinfo"
)

func main() {
	if err := json.NewEncoder(os.Stdout).Encode(buildinfo.Get("relay-api")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
