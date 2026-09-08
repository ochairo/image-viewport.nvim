.PHONY: build format format-check lint static test native-test typecheck upstream-test check

NVIM ?= nvim
export NVIM
export GOENV := off
export GOWORK := off
export GOFLAGS :=
export GOTOOLCHAIN := local
export GOTELEMETRY := off
export GOPROXY := off
export GOSUMDB := off
export GOROOT := $(shell go env GOROOT)
LUA_FILES := $(shell find lua tests -type f -name '*.lua' | sort)

build:
	./scripts/build

format:
	gofmt -w $$(find tools -type f -name '*.go')
	stylua $(LUA_FILES)

format-check:
	@command -v gofmt >/dev/null
	@files="$$(gofmt -l $$(find tools -type f -name '*.go'))" && test -z "$$files"
	stylua --check $(LUA_FILES)

lint:
	go -C tools vet ./...
	luacheck lua tests

static:
	sh tests/go-supervise.sh
	go -C tools test ./...
	./scripts/check-source

typecheck:
	./scripts/typecheck

test:
	./scripts/tool plugin-tool test "$(CURDIR)"

native-test:
	sh tests/native-processor.sh

upstream-test:
	./scripts/tool plugin-tool upstream "$(CURDIR)"

check: static format-check lint typecheck test upstream-test
