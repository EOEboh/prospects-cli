# prospect — task runner.
#
# Run `make` on its own to list every target with a one-line description.
# Targets that need input take variables, e.g. `make seed CSV=businesses.csv`.

BINARY  := prospect
PKG     := ./cmd/prospect
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/EOEboh/prospects-cli/internal/config.Version=$(VERSION)

# Where the binary lands. Every workflow target runs it from here, so they all
# work straight after `make build` without needing it on PATH.
BIN := ./$(BINARY)

DB      ?= ./prospect.db
CACHE   ?= ./cache.db
BACKUP  ?= ./backups

.DEFAULT_GOAL := help

# ---------------------------------------------------------------------------
# Guards
# ---------------------------------------------------------------------------

# require-var fails with a usable message instead of running the command with
# an empty flag and getting a confusing error from the CLI.
#
# The usage line is echoed inside single quotes because the examples contain
# double quotes; wrapping in double quotes would nest them and break the shell.
define require-var
@if [ -z "$($(1))" ]; then \
	echo 'error: $(1) is required.'; \
	echo 'usage: make $(2)'; \
	exit 1; \
fi
endef

# ---------------------------------------------------------------------------
# Build and development
# ---------------------------------------------------------------------------

.PHONY: help
help: ## List every target
	@echo "prospect — make targets"
	@echo ""
	@awk 'BEGIN {FS = ":.*?## "} \
		/^# =+$$/ { next } \
		/^# [A-Z]/ && !seen[$$0]++ { section = substr($$0, 3); next } \
		/^[a-zA-Z0-9_-]+:.*?## / { \
			if (section != "" && section != last) { printf "\n  \033[1m%s\033[0m\n", section; last = section } \
			printf "    \033[36m%-24s\033[0m %s\n", $$1, $$2 \
		}' $(MAKEFILE_LIST)
	@echo ""
	@echo "  Variables: DB=$(DB)  VERSION=$(VERSION)"
	@echo ""

.PHONY: build
build: ## Compile the binary for this machine
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)
	@echo "built $(BINARY) $(VERSION)"

.PHONY: build-linux
build-linux: ## Cross-compile for a linux/amd64 server (CGo-free)
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINARY)-linux-amd64 $(PKG)
	@echo "built $(BINARY)-linux-amd64 $(VERSION)"

.PHONY: install
install: ## Install to GOPATH/bin so `prospect` works anywhere
	go install -ldflags "$(LDFLAGS)" $(PKG)

.PHONY: test
test: ## Run all tests
	go test ./...

.PHONY: test-race
test-race: ## Run tests with the race detector on the concurrent packages
	go test -race ./internal/httpx/ ./internal/cli/ ./internal/source/website/

.PHONY: cover
cover: ## Show test coverage per package, then the total
	@go test ./... -coverprofile=coverage.out >/dev/null
	@go tool cover -func=coverage.out | tail -1
	@go test ./... -cover 2>&1 | grep -E 'coverage:' | sed 's|github.com/EOEboh/prospects-cli/||'

.PHONY: cover-html
cover-html: ## Open the coverage report in a browser
	go test ./... -coverprofile=coverage.out >/dev/null
	go tool cover -html=coverage.out

.PHONY: fmt
fmt: ## Format all Go source
	gofmt -w .

.PHONY: check
check: ## Everything CI would run: format check, vet, tests
	@test -z "$$(gofmt -l .)" || { echo "unformatted files:"; gofmt -l .; exit 1; }
	go vet ./...
	go test ./...
	@echo "all checks passed"

.PHONY: clean
clean: ## Remove build artefacts and the coverage profile
	rm -f $(BINARY) $(BINARY)-linux-amd64 coverage.out

# ---------------------------------------------------------------------------
# Setup
# ---------------------------------------------------------------------------

.PHONY: setup
setup: build env weights ## First-time setup: build, create .env and weights.yaml
	@echo ""
	@echo "Next: edit .env and set PROSPECT_USER_AGENT_EMAIL, then run 'make seed CSV=...'"

.PHONY: env
env: ## Create .env from the example (never overwrites an existing one)
	@if [ -f .env ]; then \
		echo ".env already exists, leaving it alone"; \
	else \
		cp .env.example .env; \
		echo "created .env — set PROSPECT_USER_AGENT_EMAIL before enriching"; \
	fi

.PHONY: weights
weights: build ## Write weights.yaml from the built-in defaults (never overwrites)
	@if [ -f weights.yaml ]; then \
		echo "weights.yaml already exists, leaving it alone"; \
	else \
		$(BIN) score --print-config > weights.yaml; \
		echo "created weights.yaml — tune the points as you learn what converts"; \
	fi

# ---------------------------------------------------------------------------
# Daily workflow
# ---------------------------------------------------------------------------

.PHONY: morning
morning: ## THE DAILY ONE: enrich anything new, rescore, print the brief
	@$(BIN) enrich --all-pending
	@$(BIN) score
	@echo ""
	@$(BIN) brief

.PHONY: seed
seed: ## Import businesses from a CSV — make seed CSV=businesses.csv
	$(call require-var,CSV,seed CSV=businesses.csv)
	$(BIN) seed --csv $(CSV)

.PHONY: seed-dry
seed-dry: ## Preview a CSV import without writing — make seed-dry CSV=businesses.csv
	$(call require-var,CSV,seed-dry CSV=businesses.csv)
	$(BIN) seed --csv $(CSV) --dry-run

.PHONY: enrich
enrich: ## Fetch pending business websites and extract signals (free)
	$(BIN) enrich --all-pending

.PHONY: enrich-one
enrich-one: ## Re-fetch a single business — make enrich-one ID=42
	$(call require-var,ID,enrich-one ID=42)
	$(BIN) enrich --business-id $(ID) --force

.PHONY: score
score: ## Recompute every score from current signals
	$(BIN) score

.PHONY: score-why
score-why: ## Score and print each business's full breakdown
	$(BIN) score --explain

.PHONY: brief
brief: ## The morning read: top uncontacted prospects with reasoning
	$(BIN) brief

.PHONY: list
list: ## Ranked prospects — make list [MIN=60] [STATUS=not_contacted]
	$(BIN) list $(if $(MIN),--min-score $(MIN)) $(if $(STATUS),--status $(STATUS)) $(if $(LIMIT),--limit $(LIMIT))

.PHONY: todo
todo: ## Prospects still needing a manual ad check — the highest-value work
	$(BIN) list --needs-ad-check

# ---------------------------------------------------------------------------
# Recording what you find
# ---------------------------------------------------------------------------

.PHONY: ads
ads: ## Record an ad check — make ads ID=42 VALUE=true
	$(call require-var,ID,ads ID=42 VALUE=true)
	$(call require-var,VALUE,ads ID=42 VALUE=true)
	$(BIN) signal $(ID) --type running_ads --value $(VALUE) --note "$(or $(NOTE),checked ad library)"

.PHONY: signal
signal: ## Record any signal — make signal ID=42 TYPE=hiring_lead_role VALUE=true
	$(call require-var,ID,signal ID=42 TYPE=running_ads VALUE=true)
	$(call require-var,TYPE,signal ID=42 TYPE=running_ads VALUE=true)
	$(call require-var,VALUE,signal ID=42 TYPE=running_ads VALUE=true)
	$(BIN) signal $(ID) --type $(TYPE) --value $(VALUE) $(if $(NOTE),--note "$(NOTE)")

.PHONY: signal-types
signal-types: ## List every signal type you can record
	@$(BIN) signal --help

.PHONY: status
status: ## Show or set outreach status — make status ID=42 [SET=emailed_1] [NOTE="..."]
	$(call require-var,ID,status ID=42 SET=emailed_1)
	$(BIN) status $(ID) $(if $(SET),--set $(SET)) $(if $(NOTE),--note "$(NOTE)")

.PHONY: suppress
suppress: ## Permanently exclude a business — make suppress ID=42 REASON="asked to be removed"
	$(call require-var,ID,suppress ID=42 REASON="asked to be removed")
	$(call require-var,REASON,suppress ID=42 REASON="asked to be removed")
	$(BIN) suppress $(ID) --reason "$(REASON)"

# ---------------------------------------------------------------------------
# Export
# ---------------------------------------------------------------------------

.PHONY: export
export: ## Export to CSV — make export [OUT=prospects.csv] [MIN=60]
	$(BIN) export --format csv $(if $(MIN),--min-score $(MIN)) $(if $(OUT),--out $(OUT))

.PHONY: export-json
export-json: ## Export to JSON — make export-json [OUT=prospects.json] [MIN=60]
	$(BIN) export --format json $(if $(MIN),--min-score $(MIN)) $(if $(OUT),--out $(OUT))

# ---------------------------------------------------------------------------
# Paid discovery (optional — needs GOOGLE_PLACES_API_KEY)
# ---------------------------------------------------------------------------

.PHONY: discover-dry
discover-dry: ## ALWAYS RUN FIRST: price a search — make discover-dry NICHE="recruiting agency" LOCATION="Austin, TX"
	$(call require-var,NICHE,discover-dry NICHE="recruiting agency" LOCATION="Austin, TX")
	$(call require-var,LOCATION,discover-dry NICHE="recruiting agency" LOCATION="Austin, TX")
	$(BIN) discover --niche "$(NICHE)" --location "$(LOCATION)" --limit $(or $(LIMIT),50) --dry-run

.PHONY: discover
discover: ## Spend quota to find businesses — make discover NICHE="..." LOCATION="..."
	$(call require-var,NICHE,discover NICHE="recruiting agency" LOCATION="Austin, TX")
	$(call require-var,LOCATION,discover NICHE="recruiting agency" LOCATION="Austin, TX")
	$(BIN) discover --niche "$(NICHE)" --location "$(LOCATION)" --limit $(or $(LIMIT),50)

.PHONY: quota
quota: ## Billable API calls used this month against the ceiling
	$(BIN) quota

# ---------------------------------------------------------------------------
# Database
# ---------------------------------------------------------------------------

.PHONY: db
db: ## Open a SQLite shell on the prospect database
	sqlite3 $(DB)

.PHONY: signals
signals: ## Show current signals for a business — make signals ID=42
	$(call require-var,ID,signals ID=42)
	@sqlite3 -column -header $(DB) \
		"SELECT type, value, round(confidence,2) AS conf, source, substr(detail,1,60) AS detail \
		 FROM v_current_signals WHERE business_id = $(ID) ORDER BY type;"

.PHONY: history
history: ## Show a business's signal history, including superseded values — make history ID=42
	$(call require-var,ID,history ID=42)
	@sqlite3 -column -header $(DB) \
		"SELECT type, value, source, observed_at, \
		 CASE WHEN superseded_at IS NULL THEN 'current' ELSE 'superseded' END AS state \
		 FROM signals WHERE business_id = $(ID) ORDER BY observed_at, id;"

.PHONY: backup
backup: ## Snapshot the prospect database (cache.db is disposable, so not included)
	@mkdir -p $(BACKUP)
	@sqlite3 $(DB) ".backup '$(BACKUP)/prospect-$$(date +%F-%H%M).db'"
	@echo "backed up to $(BACKUP)/prospect-$$(date +%F-%H%M).db"

.PHONY: cache-clear
cache-clear: ## Delete the HTTP cache to force refetching (never touches prospect data)
	rm -f $(CACHE) $(CACHE)-wal $(CACHE)-shm
	@echo "cache cleared — the next enrich will refetch"

.PHONY: reset
reset: ## DESTRUCTIVE: delete the prospect database and start over
	@printf "This deletes $(DB) and every signal, score and outreach note in it.\nType 'yes' to confirm: "
	@read ans; [ "$$ans" = "yes" ] || { echo "aborted"; exit 1; }
	rm -f $(DB) $(DB)-wal $(DB)-shm
	@echo "deleted $(DB)"
