#!/usr/bin/env bash
set -euo pipefail

SUBSCRIPTION_ID="${SUBSCRIPTION_ID:?Set SUBSCRIPTION_ID to the approved Azure subscription GUID}"
RUN_ID="${RUN_ID:?Set RUN_ID to the exact provisioned run ID}"
DELETE_AZURE_RESOURCES="${DELETE_AZURE_RESOURCES:-}"
RESOURCE_GROUP="rg-aks-burner-kata-acstor-${RUN_ID}"

if [[ ! "$RUN_ID" =~ ^[a-z0-9]{4,12}$ ]]; then
  printf 'RUN_ID must contain 4-12 lowercase letters or digits.\n' >&2
  exit 1
fi
if [[ "$DELETE_AZURE_RESOURCES" != "yes" ]]; then
  printf 'Set DELETE_AZURE_RESOURCES=yes to delete %s.\n' "$RESOURCE_GROUP" >&2
  exit 1
fi
if [[ "$(az account show --query id --output tsv)" != "$SUBSCRIPTION_ID" ]]; then
  printf 'Active subscription does not match SUBSCRIPTION_ID.\n' >&2
  exit 1
fi

EXPECTED_OWNER="$(az account show --query user.name --output tsv)"
GROUP_RUN_ID="$(az group show --subscription "$SUBSCRIPTION_ID" --name "$RESOURCE_GROUP" --query 'tags."aks-burner-run-id"' --output tsv)"
GROUP_OWNER="$(az group show --subscription "$SUBSCRIPTION_ID" --name "$RESOURCE_GROUP" --query 'tags."aks-burner-owner"' --output tsv)"
if [[ "$GROUP_RUN_ID" != "$RUN_ID" || "$GROUP_OWNER" != "$EXPECTED_OWNER" ]]; then
  printf 'Ownership tags do not match; refusing to delete %s.\n' "$RESOURCE_GROUP" >&2
  exit 1
fi

az group delete --subscription "$SUBSCRIPTION_ID" --name "$RESOURCE_GROUP" --yes --no-wait
