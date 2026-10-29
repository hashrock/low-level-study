.PHONY: build test vet examples
build:
	go build -o minirt .
test:
	go vet ./... && go test ./...
examples: build
	./minirt examples/hello.js
	./minirt examples/timers.js
	./minirt examples/async_fs.js --allow-read=examples/data --allow-write=examples/data
	-./minirt examples/sandbox_escape.js --allow-read=examples/data --timeout=2s
	-./minirt examples/fetch.js --allow-net=example.com
