PREFIX ?= /usr/local
BINDIR ?= $(PREFIX)/bin
LDFLAGS := -s -w

.PHONY: all web build install test fuzz bench completions clean

all: build

web:
	cd web && npm install --no-audit --no-fund && npm run build

build: web
	go build -trimpath -ldflags="$(LDFLAGS)" -o vtd ./cmd/daemon
	go build -trimpath -ldflags="$(LDFLAGS)" -o vt ./cmd/cli

install: build
	install -d "$(DESTDIR)$(BINDIR)"
	install -m 0755 vtd "$(DESTDIR)$(BINDIR)/vtd"
	install -m 0755 vt "$(DESTDIR)$(BINDIR)/vt"

completions: build
	mkdir -p deploy/completions
	./vt completion bash > deploy/completions/vt.bash
	./vt completion zsh > deploy/completions/_vt
	./vt completion fish > deploy/completions/vt.fish
	./vt completion powershell > deploy/completions/vt.ps1

test:
	go test -v ./...

fuzz:
	go test -fuzz=FuzzBencodeAndMetainfoParser -fuzztime=5s ./internal/engine

bench:
	go test -bench=BenchmarkHighThroughputSwarmTransfer -benchmem ./internal/engine

clean:
	rm -f vtd vt vtd.exe vt.exe
	rm -rf web/dist deploy/completions
