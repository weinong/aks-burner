# Per-User Azure Resource Names Design

## Goal

Allow different Azure users to provision and operate the same test suite in one subscription without sharing or deleting each other's Azure resources.

This design provides per-user isolation. It does not support concurrent independent environments for the same user and suite.

## Default Identity

When a lifecycle command needs a default resource group, `perf-runner` obtains the signed-in Azure identity with:

```text
az account show --query user.name --output tsv
```

The value must be a user principal name containing `@`. The alias is the local part before the first `@`.

The alias is normalized for resource naming by:

1. Converting it to lowercase.
2. Replacing each run of characters other than lowercase ASCII letters, digits, and hyphens with one hyphen.
3. Trimming leading and trailing hyphens.
4. Rejecting an empty result.

For example, `Jane.Doe+Perf@contoso.com` becomes `jane-doe-perf`.

If Azure CLI identity lookup fails, the account name is not a UPN, or normalization produces an empty alias, the command fails before creating, reading, or deleting Azure resources. The error directs the user to sign in with a user identity or supply an explicit resource group.

Service principal and managed identity account names are not used to derive defaults because they do not provide the requested human per-user isolation.

## Resource Names

The default resource group is:

```text
rg-aks-burner-<suite>-<alias>
```

The default AKS cluster name is derived from the suite and alias using the existing AKS-safe validation, truncation, and hash behavior. Its logical input is:

```text
<suite>-<alias>
```

An explicit cluster-name override remains unchanged.

The ARM deployment name remains `aks-burner`. Deployment names are scoped to a resource group, so distinct per-user resource groups prevent users from replacing each other's concurrent deployments.

The Azure Container Registry naming expression remains unchanged:

```bicep
'acr${take(uniqueString(resourceGroup().id, clusterName), 18)}'
```

The per-user resource-group ID and cluster name produce a distinct deterministic registry name. The existing hash also satisfies ACR's lowercase alphanumeric format and global namespace requirement.

AKS-managed resources, identities, node pools, and role assignments are contained in or derived from the isolated AKS deployment and therefore require no separate alias parameter.

## Command Behavior

The behavior applies consistently to `provision`, managed-cluster `run-suite`, and `destroy`:

- When the caller supplies `--resource-group`, use it verbatim and do not query the Azure identity for naming.
- When the caller omits `--resource-group`, derive the alias and default resource group.
- When the caller supplies `--cluster-name`, use it verbatim after existing validation.
- When the caller omits `--cluster-name`, derive it from the suite and alias when using the default resource group.

`run-suite --kube-context` retains its existing behavior. It does not need a resource group unless image builds require the managed ACR. If a resource group is required and omitted, the command derives the per-user default.

The Makefile stops materializing the old shared resource-group default. It forwards an explicit `RESOURCE_GROUP` only when the caller set one; otherwise `perf-runner` owns default derivation. This keeps direct CLI and Makefile behavior identical.

## Destroy Safety

Without the override flag, `destroy` accepts only the current user's derived default resource group for the selected suite. This preserves the existing naming guard while making it user-specific.

An explicit nondefault resource group still requires `--allow-non-default-resource-group`. Supplying that flag is the deliberate escape hatch for custom or legacy environments.

Identity lookup and default-name derivation occur before issuing `az group delete`.

## Compatibility

Existing shared defaults such as `rg-aks-burner-kata-perf` are not silently selected after this change. Users can continue operating them by passing the resource group explicitly, subject to the existing destroy override requirement.

Explicit resource-group and cluster-name inputs retain their current values and semantics. No deployed resource is renamed in place.

## Testing

Unit tests cover UPN normalization, invalid identities, lifecycle defaults, explicit overrides, destroy guards, Makefile forwarding, and unchanged ACR derivation. The full Go test suite must pass after implementation.

## Out of Scope

- Multiple concurrent Azure environments for the same user and suite.
- Kubernetes resource isolation between runs sharing one cluster.
- Local Prometheus port, result-directory, or kubeconfig concurrency.
- Automatic cleanup or migration of legacy shared resource groups.
