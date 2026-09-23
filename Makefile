# Cross-compiled binaries land in the repo root as hyperion-<os>-<arch>,
# which .gitignore already covers (/hyperion-*).

GOFLAGS := -trimpath
export CGO_ENABLED := 0

.PHONY: build mac mac-arm64 mac-amd64 clean

build:
	go build $(GOFLAGS) -o hyperion .

mac: mac-arm64 mac-amd64

mac-arm64:
	GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -o hyperion-darwin-arm64 .

mac-amd64:
	GOOS=darwin GOARCH=amd64 go build $(GOFLAGS) -o hyperion-darwin-amd64 .

clean:
	rm -f hyperion hyperion-darwin-arm64 hyperion-darwin-amd64
