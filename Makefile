SHELL := /bin/sh
.DEFAULT_GOAL := help

BINARY := orq
BIN_DIR ?= ./bin
INSTALL_DIR ?= $(HOME)/.local/bin
BUILD_TARGET := ./cmd/$(BINARY)
COMPLETIONS_DIR ?= ./completions
MAN_DIR ?= ./man
BARTOLO ?= bartolo

.PHONY: help build install-local tidy doctor completions man sync

help:
	@printf '%s\n' \
		'Available targets:' \
		'  make build          Build ./bin/$(BINARY)' \
		'  make install-local  Install $(BINARY) into $(INSTALL_DIR)' \
		'  make tidy           Run go mod tidy' \
		'  make doctor         Run the CLI doctor command' \
		'  make completions    Generate shell completions into $(COMPLETIONS_DIR)' \
		'  make man            Generate man pages into $(MAN_DIR)' \
		'  make sync           Refresh Bartolo-owned scaffold files'

build:
	@mkdir -p "$(BIN_DIR)"
	@go build -o "$(BIN_DIR)/$(BINARY)" "$(BUILD_TARGET)"
	@printf 'Built %s\n' "$(BIN_DIR)/$(BINARY)"

install-local:
	@mkdir -p "$(INSTALL_DIR)"
	@go build -o "$(INSTALL_DIR)/$(BINARY)" "$(BUILD_TARGET)"
	@printf 'Installed %s to %s/%s\n' "$(BINARY)" "$(INSTALL_DIR)" "$(BINARY)"

tidy:
	@go mod tidy

doctor:
	@go run "$(BUILD_TARGET)" --json doctor

completions:
	@mkdir -p "$(COMPLETIONS_DIR)"
	@go run "$(BUILD_TARGET)" completion bash > "$(COMPLETIONS_DIR)/$(BINARY).bash"
	@go run "$(BUILD_TARGET)" completion zsh > "$(COMPLETIONS_DIR)/_$(BINARY)"
	@go run "$(BUILD_TARGET)" completion fish > "$(COMPLETIONS_DIR)/$(BINARY).fish"
	@go run "$(BUILD_TARGET)" completion powershell > "$(COMPLETIONS_DIR)/$(BINARY).ps1"
	@printf 'Generated completions in %s\n' "$(COMPLETIONS_DIR)"

man:
	@go run "$(BUILD_TARGET)" man-pages --dir "$(MAN_DIR)"
	@printf 'Generated man pages in %s\n' "$(MAN_DIR)"

sync:
	@$(BARTOLO) sync
