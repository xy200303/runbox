VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GO ?= go
BINARY := bin/runboxd$(shell $(GO) env GOEXE)

.PHONY: build test test-race fuzz vet fmt fmt-check check run docker docker-minimal clean

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BINARY) ./cmd/runboxd

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

fuzz:
	for name in FuzzDecodeFile FuzzNormalizeRel FuzzSanitizeFileName FuzzValidIdentity; do 		$(GO) test ./sandbox -run=^$$ -fuzz=$$name -fuzztime=30s || exit 1; 	done

vet:
	$(GO) vet ./...

fmt:
	gofmt -w .

fmt-check:
	test -z "$$(gofmt -l .)"

check: fmt-check vet test-race

# Local development: no API key, data under ./.runbox. Never expose this mode.
run: build
	RUNBOX_ROOT=./.runbox RUNBOX_ALLOW_ANONYMOUS=1 ./$(BINARY)

docker:
	docker build --build-arg VERSION=$(VERSION) -t runbox:$(VERSION) -t runbox:latest .

docker-minimal:
	docker build --target minimal --build-arg VERSION=$(VERSION) -t runbox:$(VERSION)-minimal .

clean:
	rm -rf bin .runbox
