# grid-cli — build and install from source.
#
#   make install     build and install `grid`, and put its directory on your PATH
#   make             show every target
#
# Install location follows the same precedence as install.sh, so the two agree:
#   GRID_INSTALL_DIR  (if set)  >  /usr/local/bin (if writable)  >  ~/.local/bin
# Nothing here ever runs sudo. If you want /usr/local/bin and it is root-owned,
# either chown it yourself or set GRID_INSTALL_DIR.

BINARY  := grid
MODULE  := github.com/anshul-kh/grid-cli
VARS    := $(MODULE)/internal/buildinfo

# Version metadata, injected the same way .goreleaser.yaml does it so a
# `make install` build reports its provenance exactly like a released one.
# `git describe` gives e.g. v0.3.0-2-gdeadbee-dirty for an untagged tree.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X $(VARS).Version=$(VERSION) \
	-X $(VARS).Commit=$(COMMIT) \
	-X $(VARS).Date=$(DATE)

# Resolved once, here, so `install`, `uninstall` and the PATH check all agree.
INSTALL_DIR ?= $(shell \
	if [ -n "$$GRID_INSTALL_DIR" ]; then echo "$$GRID_INSTALL_DIR"; \
	elif [ -w /usr/local/bin ] 2>/dev/null; then echo /usr/local/bin; \
	else echo "$$HOME/.local/bin"; fi)

# Markers make the PATH edit idempotent and removable. The leading '#' is
# added in the shell, never here: in a Makefile a '#' starts a comment, so
# embedding it would silently define these as empty — which made the grep
# match everything and left `unset-path` unable to find the block.
MARKER_OPEN  := >>> grid-cli >>>
MARKER_CLOSE := <<< grid-cli <<<

.DEFAULT_GOAL := help
.PHONY: help build install uninstall ensure-path unset-path test test-race lint fmt vet tidy clean snapshot doctor

help: ## Show this help
	@echo "grid-cli — make targets"
	@echo
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## /{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@echo
	@echo "  install dir : $(INSTALL_DIR)"
	@echo "  version     : $(VERSION)"

build: ## Build ./grid in the repo root
	@go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) .
	@echo "built ./$(BINARY) ($(VERSION))"

install: build ensure-path ## Build, install, and add the install dir to your PATH
	@mkdir -p "$(INSTALL_DIR)"
	@install -m 0755 $(BINARY) "$(INSTALL_DIR)/$(BINARY)"
	@rm -f $(BINARY)
	@echo "installed $(INSTALL_DIR)/$(BINARY)"
	@# Resolve through PATH to catch a *different* grid shadowing this one.
	@if command -v $(BINARY) >/dev/null 2>&1; then \
		found="$$(command -v $(BINARY))"; \
		if [ "$$found" != "$(INSTALL_DIR)/$(BINARY)" ]; then \
			echo; \
			echo "warning: '$(BINARY)' on your PATH resolves to $$found,"; \
			echo "         not the copy just installed at $(INSTALL_DIR)/$(BINARY)."; \
			echo "         Remove the other one, or reorder your PATH."; \
		else \
			echo "verified: $$($(BINARY) --version 2>/dev/null || echo "$$found")"; \
		fi; \
	fi

ensure-path: ## Add the install dir to PATH in your shell rc (idempotent)
	@dir="$(INSTALL_DIR)"; \
	case ":$$PATH:" in \
		*":$$dir:"*) echo "PATH already contains $$dir"; exit 0 ;; \
	esac; \
	shell_name="$$(basename "$${SHELL:-sh}")"; \
	case "$$shell_name" in \
		zsh)  rc="$$HOME/.zshrc" ;; \
		bash) if [ -f "$$HOME/.bashrc" ]; then rc="$$HOME/.bashrc"; else rc="$$HOME/.bash_profile"; fi ;; \
		fish) rc="$$HOME/.config/fish/config.fish" ;; \
		*)    rc="$$HOME/.profile" ;; \
	esac; \
	mkdir -p "$$(dirname "$$rc")"; \
	touch "$$rc"; \
	if grep -qF '# $(MARKER_OPEN)' "$$rc" 2>/dev/null; then \
		echo "PATH block already present in $$rc (not added twice)"; \
	else \
		{ \
			echo ''; \
			echo '# $(MARKER_OPEN)'; \
			if [ "$$shell_name" = "fish" ]; then \
				echo "fish_add_path $$dir"; \
			else \
				echo "export PATH=\"$$dir:\$$PATH\""; \
			fi; \
			echo '# $(MARKER_CLOSE)'; \
		} >> "$$rc"; \
		echo "added $$dir to PATH in $$rc"; \
		echo; \
		echo "Run this now, or open a new terminal:"; \
		echo "    source $$rc"; \
	fi

unset-path: ## Remove the PATH block this Makefile added
	@for rc in "$$HOME/.zshrc" "$$HOME/.bashrc" "$$HOME/.bash_profile" "$$HOME/.profile" "$$HOME/.config/fish/config.fish"; do \
		[ -f "$$rc" ] || continue; \
		grep -qF '# $(MARKER_OPEN)' "$$rc" || continue; \
		tmp="$$(mktemp)"; \
		awk '/^# $(MARKER_OPEN)$$/{skip=1} !skip{print} /^# $(MARKER_CLOSE)$$/{skip=0}' "$$rc" > "$$tmp" && mv "$$tmp" "$$rc"; \
		echo "removed the grid-cli PATH block from $$rc"; \
	done

uninstall: unset-path ## Remove the binary and the PATH block
	@rm -f "$(INSTALL_DIR)/$(BINARY)" && echo "removed $(INSTALL_DIR)/$(BINARY)" || true

doctor: ## Show where grid resolves from and what it reports
	@echo "install dir : $(INSTALL_DIR)"
	@printf 'on PATH     : '; command -v $(BINARY) 2>/dev/null || echo '(not found — run: make install)'
	@printf 'version     : '; $(BINARY) --version 2>/dev/null || echo '(n/a)'

test: ## Run the test suite
	@go test ./...

test-race: ## Run the test suite with the race detector
	@go test -race ./...

lint: ## Run golangci-lint if installed, else vet
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run; else echo "golangci-lint not installed — running go vet"; go vet ./...; fi

fmt: ## Format the tree
	@gofmt -w .

vet: ## Run go vet
	@go vet ./...

tidy: ## Tidy go.mod
	@go mod tidy

clean: ## Remove build artifacts
	@rm -f $(BINARY)
	@rm -rf dist
	@echo "cleaned"

snapshot: ## Build a local GoReleaser snapshot (requires goreleaser)
	@command -v goreleaser >/dev/null 2>&1 || { echo "goreleaser is not installed: https://goreleaser.com/install/"; exit 1; }
	@goreleaser release --snapshot --clean
