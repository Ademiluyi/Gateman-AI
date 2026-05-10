# GatemanAI build targets.
#
# `make build`     compiles the laptop binaries (gatemanai, mcpserver, doctor) for the host.
# `make cross`     cross-compiles picapture for both Pi 3 B+ (ARMv7) and Pi 4/5 (ARM64).
# `make test`      runs the full Go test suite.
# `make release`   produces a dist/ directory with all artifacts ready to upload to GitHub Releases.

.PHONY: all build cross cross-arm32 cross-arm64 test vet release clean

# -trimpath + -s -w strip the DWARF symbol table and the LC_UUID load
# command. On macOS that combination makes dyld refuse to run the binary
# (older Go toolchains especially). Use neither for the host build; both
# are safe on Linux cross-builds where they shave ~1-2MB.
LINUX_GOFLAGS := -trimpath
LINUX_LDFLAGS := -s -w
DIST          := dist

all: build cross

build:
	go build -o gatemanai  ./cmd/gatemanai
	go build -o mcpserver  ./cmd/mcpserver
	go build -o doctor     ./cmd/doctor
	go build -o allow      ./cmd/allow

cross: cross-arm32 cross-arm64

cross-arm32:
	GOOS=linux GOARCH=arm   GOARM=7 go build $(LINUX_GOFLAGS) -ldflags="$(LINUX_LDFLAGS)" -o picapture_arm32 ./cmd/picapture

cross-arm64:
	GOOS=linux GOARCH=arm64         go build $(LINUX_GOFLAGS) -ldflags="$(LINUX_LDFLAGS)" -o picapture_arm64 ./cmd/picapture

test:
	go test ./...

vet:
	go vet ./...

release: clean test vet build cross
	mkdir -p $(DIST)
	cp gatemanai mcpserver doctor allow picapture_arm32 picapture_arm64 $(DIST)/
	@echo "Release artefacts ready in $(DIST)/"

clean:
	rm -f gatemanai mcpserver doctor allow picapture_arm32 picapture_arm64
	rm -rf $(DIST)
