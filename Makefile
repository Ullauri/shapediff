# shapediff build and test. packages.Load shells out to `go`, so the toolchain
# must be on PATH for every test that runs the extractor.
export PATH := $(PATH):/usr/local/go/bin:$(HOME)/go/bin
export GOFLAGS := -mod=mod

.PHONY: build test vet conform fmt

build:
	go build -o bin/shapediff ./cmd/shapediff

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

conform: build
	./bin/shapediff conform --fixtures conformance/go

check: fmt vet test conform
