lint:
	golangci-lint run -c ./golangci.yml ./...

build:
	go build -o pocketbase-mysql ./examples/base

test:
	go test ./... -v --cover

jstypes:
	go run ./plugins/jsvm/internal/types/types.go

test-report:
	go test ./... -v --cover -coverprofile=coverage.out
	go tool cover -html=coverage.out
