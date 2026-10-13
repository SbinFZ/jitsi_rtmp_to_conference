VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test lint verify package notices docker clean
build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/jitsi-stream ./cmd/jitsi-stream
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/jitsi-receive ./cmd/jitsi-receive
test:
	go test -race ./...
	cd third_party/jitsi && go test -race ./...
lint:
	test -z "$$(gofmt -l cmd internal third_party/jitsi)"
	go vet ./...
# End-to-end against a real Jitsi (JITSI_HOST, default m.yuanjinapp.com).
verify:
	scripts/verify.sh
# All OS/arch archives + SHA256SUMS in dist/ (same script as CI).
package:
	VERSION=$(VERSION) scripts/package.sh
notices:
	python3 scripts/third-party-notices.py
docker:
	docker build --build-arg VERSION=$(VERSION) -t jitsi-stream:$(VERSION) .
clean:
	rm -rf bin dist
