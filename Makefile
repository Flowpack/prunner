all: build_linux_amd64 build_darwin_amd64 copy

build_linux_amd64:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags='-extldflags=-static -s -w' -o bin/prunner-linux-amd64 ./cmd/prunner
	upx bin/prunner-linux-amd64

build_darwin_amd64:
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -ldflags='-extldflags=-static -s -w' -o bin/prunner-darwin-amd64 ./cmd/prunner
	upx bin/prunner-darwin-amd64

# TODO Add more build targets

copy:
	mkdir -p ../neos/DistributionPackages/Flowpack.Prunner/Resources/Private/Bin/Linux/x86_64
	cp bin/prunner-linux-amd64 ../neos/DistributionPackages/Flowpack.Prunner/Resources/Private/Bin/Linux/x86_64/prunner
	mkdir -p ../neos/DistributionPackages/Flowpack.Prunner/Resources/Private/Bin/Darwin/x86_64
	cp bin/prunner-darwin-amd64 ../neos/DistributionPackages/Flowpack.Prunner/Resources/Private/Bin/Darwin/x86_64/prunner
