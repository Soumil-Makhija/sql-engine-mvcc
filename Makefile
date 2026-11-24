GO = go

.PHONY: build test check bench clean
build:
	mkdir -p build
	$(GO) build -o build/minisql ./cmd/minisql

test:
	$(GO) test ./...

check:
	$(GO) vet ./...
	$(GO) test -race ./...

bench: build
	python3 bench/compare_sqlite.py --binary build/minisql

clean:
	rm -rf build
