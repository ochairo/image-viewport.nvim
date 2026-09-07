.PHONY: format format-check lint static test native-test typecheck upstream-test check

NVIM ?= nvim
export NVIM
LUA_FILES := $(shell find lua tests -type f -name '*.lua' | sort)

format:
	stylua $(LUA_FILES)

format-check:
	stylua --check $(LUA_FILES)

lint:
	luacheck lua tests

static:
	python3 -B scripts/check-source.py
	python3 -B -m unittest discover -s tests -p 'test_*.py'

typecheck:
	python3 -B scripts/typecheck.py

test:
	python3 -B tests/run.py

native-test:
	sh tests/native-processor.sh

upstream-test:
	python3 -B scripts/upstream-test.py

check: static format-check lint typecheck test upstream-test
