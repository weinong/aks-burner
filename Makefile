TEST_MODE ?= smoke
AZURE_LOCATION ?= westus2
RESOURCE_GROUP ?=
RESOURCE_GROUP_ARG = $(if $(filter command line environment environment override,$(origin RESOURCE_GROUP)), --resource-group "$(RESOURCE_GROUP)")
CLUSTER_NAME ?=
CLUSTER_NAME_ARG = $(if $(strip $(CLUSTER_NAME)), --cluster-name "$(CLUSTER_NAME)")
KUBE_CONTEXT ?=
RUN_SUITE_CONTEXT_ARGS = $(if $(strip $(KUBE_CONTEXT)),--kube-context "$(KUBE_CONTEXT)")

.DEFAULT_GOAL := test

.PHONY: help test build list-suites add-suite add-suite-guided provision run-suite destroy clean-results

help:
	@printf '%s\n' 'Available targets:'
	@printf '  %-20s %s\n' 'help' 'Show this help message.'
	@printf '  %-20s %s\n' 'test' 'Run Go tests.'
	@printf '  %-20s %s\n' 'build' 'Build the perf-runner binary into bin/.'
	@printf '  %-20s %s\n' 'list-suites' 'List configured performance test suites.'
	@printf '  %-20s %s\n' 'add-suite' 'Create a dummy suite from TEST_SUITE.'
	@printf '  %-20s %s\n' 'add-suite-guided' 'Create a dummy suite with interactive prompts.'
	@printf '  %-20s %s\n' 'provision' 'Provision Azure infrastructure for TEST_SUITE.'
	@printf '  %-20s %s\n' 'run-suite' 'Run TEST_SUITE with kube-burner.'
	@printf '  %-20s %s\n' 'destroy' 'Destroy the default suite resource group.'
	@printf '  %-20s %s\n' 'clean-results' 'Remove generated result files.'
	@printf '\n%s\n' 'Common examples:'
	@printf '  %s\n' 'make list-suites'
	@printf '  %s\n' 'TEST_SUITE=my-suite make add-suite'
	@printf '  %s\n' 'make add-suite-guided'
	@printf '  %s\n' 'TEST_SUITE=kata-perf make provision'
	@printf '  %s\n' 'TEST_SUITE=kata-perf TEST_MODE=smoke make run-suite'
	@printf '  %s\n' 'TEST_SUITE=kata-perf make destroy'
	@printf '\n%s\n' 'Key variables:'
	@printf '  %-20s %s\n' 'TEST_SUITE' 'Required for add-suite, provision, run-suite, and destroy.'
	@printf '  %-20s %s\n' 'TEST_MODE' 'Defaults to smoke.'
	@printf '  %-20s %s\n' 'AZURE_LOCATION' 'Defaults to westus2.'
	@printf '  %-20s %s\n' 'RESOURCE_GROUP' 'Optionally overrides the per-user resource group derived by perf-runner.'
	@printf '  %-20s %s\n' 'CLUSTER_NAME' 'Optionally overrides the derived AKS cluster name.'
	@printf '  %-20s %s\n' 'KUBE_CONTEXT' 'Optionally targets an existing Kubernetes context for run-suite.'

test:
	go test ./...

build:
	go build -o bin/perf-runner ./cmd/perf-runner

list-suites:
	go run ./cmd/perf-runner list-suites

add-suite:
	@test -n "$(TEST_SUITE)" || (echo "TEST_SUITE is required" && exit 1)
	go run ./cmd/perf-runner add-suite --suite "$(TEST_SUITE)"

add-suite-guided:
	go run ./cmd/perf-runner add-suite --guided

provision:
	@test -n "$(TEST_SUITE)" || (echo "TEST_SUITE is required" && exit 1)
	go run ./cmd/perf-runner provision --suite "$(TEST_SUITE)"$(RESOURCE_GROUP_ARG) --location "$(AZURE_LOCATION)"$(CLUSTER_NAME_ARG)

run-suite:
	@test -n "$(TEST_SUITE)" || (echo "TEST_SUITE is required" && exit 1)
	go run ./cmd/perf-runner run-suite --suite "$(TEST_SUITE)" --mode "$(TEST_MODE)"$(RESOURCE_GROUP_ARG)$(CLUSTER_NAME_ARG) $(RUN_SUITE_CONTEXT_ARGS)

destroy:
	@test -n "$(TEST_SUITE)" || (echo "TEST_SUITE is required" && exit 1)
	go run ./cmd/perf-runner destroy --suite "$(TEST_SUITE)"$(RESOURCE_GROUP_ARG)

clean-results:
	rm -rf results/*
	touch results/.gitkeep
