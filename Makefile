.PHONY: build test vet clean

build:
	mkdir -p bin
	go build -buildvcs=false -trimpath -o bin/eakd ./cmd/eakd
	go build -buildvcs=false -trimpath -o bin/eakc ./cmd/eakc

test:
	go test ./...

vet:
	go vet -buildvcs=false ./...

clean:
	rm -rf bin
