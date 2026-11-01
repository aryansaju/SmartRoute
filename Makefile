# Windows users: run the commands from the README in PowerShell instead (or use WSL).
GO ?= go

.PHONY: help tidy build test race vet lint-lite py-test run-mock run-gateway up down bench-cache clean

help:
	@echo "tidy build test race vet py-test run-mock run-gateway up down bench-cache"

tidy:            ## resolve dependencies and write go.sum (commit it)
	$(GO) mod tidy

build:
	$(GO) build ./...

test:
	$(GO) test ./...

race:            ## the important one: tests under the race detector
	$(GO) test -race -count=1 ./...

vet:
	$(GO) vet ./...

lint-lite:       ## static checks that need no Go toolchain (not a compiler)
	python scripts/golint_lite.py .

py-test:
	cd sidecar/embedder && python -m unittest -q test_embedder
	cd bench/cache_quality && python -m unittest -q test_sweep
	cd bench && python -m unittest -q test_overhead_report

run-mock:
	$(GO) run ./cmd/mockupstream -latency=500ms

run-gateway:     ## in memory store and fake embedder unless DATABASE_URL / EMBEDDER_URL are set
	$(GO) run ./cmd/gateway

up:
	docker compose -f deploy/docker-compose.yml up --build

down:
	docker compose -f deploy/docker-compose.yml down

bench-cache:     ## needs: pip install sentence-transformers numpy matplotlib
	python bench/cache_quality/sweep.py --pairs bench/cache_quality/data/handwritten_pairs.csv --backend sentence-transformers --out bench/results/cache_quality

clean:
	rm -rf bin bench/results/cache_quality
