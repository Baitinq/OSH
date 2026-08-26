SHELL := /bin/sh

.PHONY: setup smoke quick run

setup:
	python3 benchmark.py setup

smoke:
	@test -n "$(LABEL)" || (echo "usage: make smoke LABEL=<name>" >&2; exit 2)
	.venv/bin/python benchmark.py run --label "$(LABEL)" --first 27 --last 27

quick:
	@test -n "$(LABEL)" || (echo "usage: make quick LABEL=<name> [HARNESS=fn|codex|pi|both]" >&2; exit 2)
	.venv/bin/python benchmark.py run --label "$(LABEL)" --harness "$(or $(HARNESS),fn)" --first 27 --last 55 --step 2

run:
	@test -n "$(LABEL)" || (echo "usage: make run LABEL=<name>" >&2; exit 2)
	.venv/bin/python benchmark.py run --label "$(LABEL)"
