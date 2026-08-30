BINARY := beads-tui
VERSION := 0.1.0

.PHONY: build test vet clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o $(BINARY) ./cmd/beads-tui

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -f $(BINARY)