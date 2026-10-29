.PHONY: build test vet examples
build:
	go build -o bin/minirt .
test:
	go vet ./... && go test ./...
examples: build
	./bin/minirt examples/hello.js
	./bin/minirt examples/timers.js
	./bin/minirt examples/async_fs.js --allow-read=examples/data --allow-write=examples/data
	-./bin/minirt examples/sandbox_escape.js --allow-read=examples/data --timeout=2s
	-./bin/minirt examples/fetch.js --allow-net=example.com
