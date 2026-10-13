GOPROXY ?= https://proxy.golang.org,direct
export GOPROXY

.PHONY: build test linux docker clean
build:
	go build -trimpath -o bin/jitsi-stream ./cmd/jitsi-stream
	go build -trimpath -o bin/jitsi-receive ./cmd/jitsi-receive
test:
	go test -race ./...
	cd third_party/jitsi && go test -race ./...
	go vet ./...
linux:
	mkdir -p dist/linux
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o dist/linux/jitsi-stream ./cmd/jitsi-stream
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o dist/linux/jitsi-receive ./cmd/jitsi-receive
docker:
	docker build -t jitsi-stream:local .
clean:
	rm -rf bin dist
