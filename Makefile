# Get the latest commit branch, hash, and date.
TAG=$(shell git describe --tags --abbrev=0 --exact-match 2>/dev/null)
BRANCH=$(if $(TAG),$(TAG),$(shell git rev-parse --abbrev-ref HEAD 2>/dev/null))
HASH=$(shell git rev-parse --short=7 HEAD 2>/dev/null)
TIMESTAMP=$(shell TZ=UTC git log -1 --date=format-local:%Y%m%dT%H%M%S --format=%cd HEAD 2>/dev/null)
GIT_REV=$(shell printf "%s-%s-%s" "$(BRANCH)" "$(HASH)" "$(TIMESTAMP)")
REV=$(if $(filter --,$(GIT_REV)),latest,$(GIT_REV))
# riverui reports this at /api/version; as a dependency it would otherwise show riverdelta's version
RIVERUI_VERSION=$(shell go list -m -f '{{.Version}}' riverqueue.com/riverui)

all: test build

build:
	mkdir -p .bin
	go build -trimpath -ldflags "-X main.revision=$(REV) -X riverqueue.com/riverui/internal/buildinfo.version=$(RIVERUI_VERSION) -s -w" -o .bin/riverdelta ./app

test:
	go clean -testcache
	go test -race -coverprofile=coverage.out ./...
	grep -v "_mock.go" coverage.out | grep -v mocks > coverage_no_mocks.out
	go tool cover -func=coverage_no_mocks.out
	rm coverage.out coverage_no_mocks.out

fmt:
	gofmt -s -w $$(find . -type f -name "*.go" -not -path "./mocks/*" -not -path "**/mocks/*")
	goimports -w $$(find . -type f -name "*.go" -not -path "./mocks/*" -not -path "**/mocks/*")

lint:
	golangci-lint run --max-issues-per-linter=0 --max-same-issues=0

race:
	go test -race -timeout=300s ./...

version:
	@echo "branch: $(BRANCH), hash: $(HASH), timestamp: $(TIMESTAMP)"
	@echo "revision: $(REV)"

clean:
	rm -rf .bin dist coverage.out

.PHONY: all build test lint fmt race version clean
