.PHONY: build fmt vet test test-dopamine install uninstall help

VERSION := $(shell awk '{print $$1}' VERSION)

build: ## Build dopamine
	go build -ldflags "-X main.Version=$(VERSION)" -o bin/dopamine ./cmd/dopamine/

fmt: ## Format Go sources
	gofmt -w cmd internal

vet: ## Check formatting and run go vet
	@test -z "$$(gofmt -l cmd internal)"
	go vet ./...

test: vet ## Run all checks, including isolated terminal scenarios
	go test -race ./...
	$(MAKE) test-dopamine

test-dopamine: build ## Check dopamine, installation and real tmux interactions
	go test ./...
	python3 -m unittest discover -s scripts -p 'test_*source.py'
	python3 -m unittest discover -s scripts -p 'test_dopamine_install.py'
	python3 scripts/dopamine-smoke.py --binary bin/dopamine

install: ## Install the source launcher
	./install.sh

uninstall: ## Remove the launcher while keeping data and processes
	./uninstall.sh

help: ## List development commands
	@sed -n 's/^\([a-z-]*\):.*## /\1: /p' Makefile

.DEFAULT_GOAL := help
