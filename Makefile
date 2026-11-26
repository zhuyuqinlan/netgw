BINARY := netgw
CMD    := ./cmd/gateway
VERSION ?= dev

.PHONY: build test race vet fmt clean linux-amd64 linux-arm64 release-zip

build:
	go build -trimpath -ldflags "-X main.version=$(VERSION)" -o $(BINARY) $(CMD)

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	go fmt ./...

clean:
	rm -f $(BINARY) netgw-*

# 交叉编译 Linux 二进制（静态编译，可直接部署到无 Go 环境的服务器）
linux-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BINARY)-linux-amd64 $(CMD)

linux-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BINARY)-linux-arm64 $(CMD)

# 本机便携包（示例：linux amd64）
release-zip:
	./scripts/build.sh "$$(go env GOOS)" "$$(go env GOARCH)" "$(VERSION)" dist/bin/netgw
	./scripts/pack-zip.sh "$$(go env GOOS)" "$$(go env GOARCH)" "$(VERSION)" dist/bin/netgw "dist/netgw-$(VERSION)-$$(go env GOOS)-$$(go env GOARCH).zip"
