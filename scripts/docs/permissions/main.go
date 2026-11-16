// Command permissions writes the required-permission table from the annotated client methods: the Go table in
// internal/harbor and the admin docs page. `make generate` runs it.
package main

import (
	"fmt"
	"os"

	"github.com/solidDoWant/harbor-operator/internal/harbor/permtable"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "permissions:", err)
		os.Exit(1)
	}
}

func run() error {
	table, err := permtable.Build("internal/harbor", "internal/controller")
	if err != nil {
		return err
	}

	source, err := permtable.GoSource(table)
	if err != nil {
		return err
	}

	if err := os.WriteFile("internal/harbor/required_permissions_generated.go", source, 0o644); err != nil {
		return err
	}

	return os.WriteFile("docs/admin/required-permissions.md", permtable.Markdown(table), 0o644)
}
