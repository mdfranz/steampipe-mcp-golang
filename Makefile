.PHONY: all build run clean test fmt vet install

APP_NAME = steampipe-mcp

all: fmt vet build

build:
	go build -o $(APP_NAME) ./cmd/steampipe-mcp

run:
	go run ./cmd/${APP_NAME}

test:
	go test -v ./...

fmt:
	go fmt ./...

vet:
	go vet ./...

install: build
	mkdir -p $(HOME)/.local/bin
	cp $(APP_NAME) $(HOME)/.local/bin/

clean:
	rm -f $(APP_NAME)
	rm -f *.lock
	rm -f *.log
