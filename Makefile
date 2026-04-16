BINARY = tabsordnung
GOFLAGS = -p 1

.PHONY: build test run clean

build:
	GOMAXPROCS=1 go build $(GOFLAGS) -o $(BINARY) .

test:
	GOMAXPROCS=1 go test $(GOFLAGS) ./... -v

run: build
	./$(BINARY)

run-live: build
	./$(BINARY) --live

run-profile: build
	TABSORDNUNG_DIAG_INTERVAL=10s  TABSORDNUNG_PPROF_ADDR=127.0.0.1:6060 ./$(BINARY) --live

clean:
	rm -f $(BINARY)

install:
	ln -sf $(PWD)/$(BINARY) ~/.local/bin/$(BINARY) 

