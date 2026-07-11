package infra

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

type OutputRunner func(context.Context, []string) ([]byte, error)

var invalidAliasCharacters = regexp.MustCompile(`[^a-z0-9-]+`)

func NormalizeAzureUserAlias(accountName string) (string, error) {
	localPart, _, ok := strings.Cut(strings.TrimSpace(accountName), "@")
	if !ok {
		return "", azureUserIdentityError(accountName)
	}
	alias := invalidAliasCharacters.ReplaceAllString(strings.ToLower(localPart), "-")
	alias = strings.Trim(alias, "-")
	if alias == "" {
		return "", azureUserIdentityError(accountName)
	}
	return alias, nil
}

func AzureUserAlias(ctx context.Context, runOutput OutputRunner) (string, error) {
	args := []string{"az", "account", "show", "--query", "user.name", "--output", "tsv"}
	if runOutput == nil {
		runOutput = func(ctx context.Context, args []string) ([]byte, error) {
			return exec.CommandContext(ctx, args[0], args[1:]...).Output()
		}
	}
	data, err := runOutput(ctx, args)
	if err != nil {
		return "", fmt.Errorf("cannot determine the signed-in Azure user; sign in with a user identity or supply an explicit resource group: %w", err)
	}
	return NormalizeAzureUserAlias(string(data))
}

func azureUserIdentityError(accountName string) error {
	return fmt.Errorf("Azure account %q is not a usable user identity; sign in with a user identity or supply an explicit resource group", strings.TrimSpace(accountName))
}
