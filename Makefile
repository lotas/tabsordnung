BINARY = tabsordnung
GOFLAGS = -p 1

.PHONY: build test run clean

build:
	GOMAXPROCS=1 go build $(GOFLAGS) -o $(BINARY) .

test:
	GOMAXPROCS=1 go test $(GOFLAGS) ./... -v

run: build
	./$(BINARY)

clean:
	rm -f $(BINARY)
