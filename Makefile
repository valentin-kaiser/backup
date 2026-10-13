# Top level helpers, each process has its own Makefile: worker/control/cmd (daemon) and worker/portal/cmd (web portal).
NPM ?= npm

.PHONY: all build frontend frontend-test proto sqlc test lint image clean

all: build

# control-portal embeds the Angular build, so it is built first. The daemon does not need it.
build: frontend
	$(MAKE) -C worker/control/cmd prod
	$(MAKE) -C worker/portal/cmd prod

frontend:
	$(NPM) --prefix worker/portal/ui ci
	$(NPM) --prefix worker/portal/ui run build

frontend-test:
	$(NPM) --prefix worker/portal/ui test -- --watch=false

# Regenerates the Go and TypeScript code of the protobuf services in a container.
proto:
	$(MAKE) -C proto generate

# Regenerates the Go code of the portal queries (worker/portal/pkg/database/queries) with sqlc.
sqlc:
	$(MAKE) -C worker/portal/pkg/database schema

test: frontend-test
	go test ./...

lint:
	$(MAKE) -C worker/control/cmd lint
	$(MAKE) -C worker/portal/cmd lint

image:
	docker build -t backup .

clean:
	rm -rf bin
