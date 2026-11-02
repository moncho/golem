.PHONY: bundle build check

bundle:
	npm ci --prefix host --ignore-scripts
	npm --prefix host run bundle

build: bundle
	go build -o bin/golem ./cmd/golem

check: build
	npm --prefix host run check
	npm --prefix host test
	go vet ./...
	go test -race ./...
	bin/golem demo
