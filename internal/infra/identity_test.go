package infra

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeAzureUserAlias(t *testing.T) {
	tests := []struct {
		name        string
		accountName string
		want        string
	}{
		{name: "simple UPN", accountName: "jane@contoso.com", want: "jane"},
		{name: "normalizes punctuation", accountName: "Jane.Doe+Perf@contoso.com", want: "jane-doe-perf"},
		{name: "collapses and trims separators", accountName: ".Jane__Doe.@contoso.com", want: "jane-doe"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeAzureUserAlias(test.accountName)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("NormalizeAzureUserAlias(%q) = %q, want %q", test.accountName, got, test.want)
			}
		})
	}
}

func TestNormalizeAzureUserAliasRejectsInvalidIdentities(t *testing.T) {
	for _, accountName := range []string{"service-principal", "@contoso.com", "...@contoso.com"} {
		t.Run(accountName, func(t *testing.T) {
			_, err := NormalizeAzureUserAlias(accountName)
			if err == nil || !strings.Contains(err.Error(), "user identity") || !strings.Contains(err.Error(), "explicit resource group") {
				t.Fatalf("NormalizeAzureUserAlias(%q) error = %v, want user identity and explicit resource group guidance", accountName, err)
			}
		})
	}
}

func TestAzureUserAliasUsesSignedInAccountUPN(t *testing.T) {
	var got []string
	alias, err := AzureUserAlias(context.Background(), func(_ context.Context, args []string) ([]byte, error) {
		got = append([]string(nil), args...)
		return []byte("Jane.Doe+Perf@contoso.com\n"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCommand := []string{"az", "account", "show", "--query", "user.name", "--output", "tsv"}
	if !reflect.DeepEqual(got, wantCommand) {
		t.Fatalf("AzureUserAlias command = %#v, want %#v", got, wantCommand)
	}
	if alias != "jane-doe-perf" {
		t.Fatalf("AzureUserAlias() = %q, want jane-doe-perf", alias)
	}
}

func TestAzureUserAliasExplainsLookupFailure(t *testing.T) {
	_, err := AzureUserAlias(context.Background(), func(context.Context, []string) ([]byte, error) {
		return nil, errors.New("not logged in")
	})
	if err == nil || !strings.Contains(err.Error(), "sign in") || !strings.Contains(err.Error(), "explicit resource group") {
		t.Fatalf("AzureUserAlias() error = %v, want sign in and explicit resource group guidance", err)
	}
}
