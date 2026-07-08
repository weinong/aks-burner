TEST_MODE ?= smoke
AZURE_LOCATION ?= westus2
RESOURCE_GROUP ?= rg-aks-burner-$(TEST_SUITE)

.PHONY: test build list-suites provision run-suite destroy clean-results

test:
	go test ./...

build:
	go build -o bin/perf-runner ./cmd/perf-runner

list-suites:
	go run ./cmd/perf-runner list-suites

provision:
	@test -n "$(TEST_SUITE)" || (echo "TEST_SUITE is required" && exit 1)
	go run ./cmd/perf-runner provision --suite "$(TEST_SUITE)" --resource-group "$(RESOURCE_GROUP)" --location "$(AZURE_LOCATION)"

run-suite:
	@test -n "$(TEST_SUITE)" || (echo "TEST_SUITE is required" && exit 1)
	go run ./cmd/perf-runner run-suite --suite "$(TEST_SUITE)" --mode "$(TEST_MODE)" --resource-group "$(RESOURCE_GROUP)"

destroy:
	@test -n "$(TEST_SUITE)" || (echo "TEST_SUITE is required" && exit 1)
	go run ./cmd/perf-runner destroy --suite "$(TEST_SUITE)" --resource-group "$(RESOURCE_GROUP)"

clean-results:
	rm -rf results/*
	touch results/.gitkeep
