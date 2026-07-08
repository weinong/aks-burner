package main

import (
	"fmt"
	"os"

	"github.com/Azure/aks-burner/internal/repo"
	"github.com/Azure/aks-burner/internal/suite"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: perf-runner <list-suites|provision|run-suite|destroy> ...")
	}
	switch args[0] {
	case "list-suites":
		return listSuites()
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func listSuites() error {
	root, err := repo.Root(".")
	if err != nil {
		return err
	}
	suites, err := suite.List(root)
	if err != nil {
		return err
	}
	for _, cfg := range suites {
		fmt.Printf("%s\t%s\n", cfg.Name, cfg.Description)
	}
	return nil
}
