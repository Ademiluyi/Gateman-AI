# GatemanAI build targets.
#
# `make build`     compiles the laptop binaries (gatemanai, mcpserver, doctor) for the host.
# `make cross`     cross-compiles picapture for both Pi 3 B+ (ARMv7) and Pi 4/5 (ARM64).
# `make test`      runs the full Go test suite.
# `make release`   produces a dist/ directory with all artifacts ready to upload to GitHub Releases.

.PHONY: all build cross cross-arm32 cross-arm64 test vet release clean

GOFLAGS  := -trimpath
LDFLAGS  := -s -w
DIST     := dist

all: build cross

build:
	go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o gatemanai  ./cmd/gatemanai
	go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o mcpserver  ./cmd/mcpserver
	go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o doctor     ./cmd/doctor

cross: cross-arm32 cross-arm64

cross-arm32:
	GOOS=linux GOARCH=arm   GOARM=7 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o picapture_arm32 ./cmd/picapture

cross-arm64:
	GOOS=linux GOARCH=arm64         go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o picapture_arm64 ./cmd/picapture

test:
	go test ./...

vet:
	go vet ./...

release: clean test vet build cross
	mkdir -p $(DIST)
	cp gatemanai mcpserver doctor picapture_arm32 picapture_arm64 $(DIST)/
	@echo "Release artefacts ready in $(DIST)/"

clean:
	rm -f gatemanai mcpserver doctor picapture_arm32 picapture_arm64
	rm -rf $(DIST)
