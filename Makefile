all: build_linux_amd64 build_darwin_amd64

build_linux_amd64:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags='-extldflags=-static -s -w' -o bin/prunner-linux-amd64 ./cmd/prunner
	upx bin/prunner-linux-amd64

build_darwin_amd64:
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -ldflags='-extldflags=-static -s -w' -o bin/prunner-darwin-amd64 ./cmd/prunner
	upx bin/prunner-darwin-amd64
