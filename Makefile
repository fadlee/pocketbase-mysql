lint:
	golangci-lint run -c ./golangci.yml ./...

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS = -ldflags "-X github.com/pocketbase/pocketbase.Version=$(VERSION)"

build:
	go build $(LDFLAGS) -o pocketbase-mysql ./examples/base

test:
	go test ./... -v --cover

jstypes:
	go run ./plugins/jsvm/internal/types/types.go

test-report:
	go test ./... -v --cover -coverprofile=coverage.out
	go tool cover -html=coverage.out
