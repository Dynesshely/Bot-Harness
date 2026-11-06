// Command configkeys prints the backend configuration key manifest as JSON.
//
// It is the callable entry point behind scripts/check-config-keys.sh: CI and the
// front-end can compare their key sets against the backend without importing Go
// code. Output is deterministic (sorted keys, two-space indent, trailing newline).
package main

import (
	"fmt"
	"os"

	"github.com/dynesshely/bot-harness/internal/settings"
)

func main() {
	raw, err := settings.ConfigKeysJSON()
	if err != nil {
		fmt.Fprintln(os.Stderr, "configkeys:", err)
		os.Exit(1)
	}
	if _, err := os.Stdout.Write(raw); err != nil {
		fmt.Fprintln(os.Stderr, "configkeys:", err)
		os.Exit(1)
	}
}
