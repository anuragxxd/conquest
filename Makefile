.PHONY: build test test-short lint fmt vet clean tidy \
	docker-server docker-nf-runner docker-nf-runner-all \
	docker-wdl-runner docker-smk-runner docker-smk-v7-runner docker-smk-runner-all docker-all \
	docker-build docker-runner-build docker-snakemake-runner-build \
	docker-snakemake-runner-build-v7 docker-snakemake-runner-build-all docker-wdl-runner-build

GO := go
MODULE := github.com/anuragxxd/conquest
REGISTRY := registry.example.com/conquest
TAG ?= latest

NXF_VER ?= 24.10.4
NXF_VERSIONS := 24.10.4 25.04.3 25.10.2 25.10.4
NXF_RUNTIME_TAG_PREFIX ?= cleanup-20260328011600
NXF_PLUGINS_ALLOWED ?= nf-ga4gh,nf-amazon,nf-schema,nf-core-utils,nf-fgbio,nf-prov

SNAKEMAKE_VER ?= 9.17.3
SNAKEMAKE_V7_VER ?= 7.32.4
MINIWDL_VER ?= 1.12.1

build:
	$(GO) build ./...

test:
	$(GO) test -v -race -count=1 ./...

test-short:
	$(GO) test -short -race ./...

lint: vet
	$(GO) vet ./...

fmt:
	gofmt -s -w .

vet:
	$(GO) vet ./...

clean:
	$(GO) clean ./...

tidy:
	$(GO) mod tidy

# Build and push the conquest server image.
docker-server:
	docker buildx build --platform linux/amd64 \
		-f docker/server/Dockerfile \
		-t $(REGISTRY)/conquest:$(TAG) \
		--push .

# Build and push one Nextflow runner image. Usage:
#   make docker-nf-runner NXF_VER=25.10.2 TAG=release-20260329
docker-nf-runner:
	@if [ -z "$(NXF_VER)" ]; then echo "NXF_VER is required"; exit 1; fi
	@case "$(NXF_VER)" in \
		24.10.4|25.04.3|25.10.2|25.10.4) NXF_BASE_IMAGE='$(REGISTRY)/conquest-runner:$(NXF_RUNTIME_TAG_PREFIX)-nf-$(NXF_VER)' ;; \
		*) echo "unsupported NXF_VER: $(NXF_VER)"; exit 1 ;; \
	esac; \
	docker buildx build --platform linux/amd64 \
		-f docker/nf-runner/Dockerfile \
		--build-arg NXF_BASE_IMAGE="$$NXF_BASE_IMAGE" \
		--build-arg NXF_PLUGINS_ALLOWED=$(NXF_PLUGINS_ALLOWED) \
		-t $(REGISTRY)/conquest-nf-$(NXF_VER)-runner:$(TAG) \
		--push .

# Build and push all supported Nextflow runner images.
docker-nf-runner-all:
	@for ver in $(NXF_VERSIONS); do \
		$(MAKE) docker-nf-runner NXF_VER=$$ver TAG=$(TAG) || exit $$?; \
	done

# Build and push the WDL (miniwdl) runner image.
docker-wdl-runner:
	docker buildx build --platform linux/amd64 \
		-f docker/wdl-runner/Dockerfile \
		-t $(REGISTRY)/conquest-wdl-runner:$(TAG) \
		--push .

# Build and push the default Snakemake runner image.
docker-smk-runner:
	docker buildx build --platform linux/amd64 \
		-f docker/smk-runner/Dockerfile \
		--build-arg SNAKEMAKE_VERSION=$(SNAKEMAKE_VER) \
		-t $(REGISTRY)/conquest-smk-$(SNAKEMAKE_VER)-runner:$(TAG) \
		--push .

# Build and push the Snakemake 7.x runner image.
docker-smk-v7-runner:
	docker buildx build --platform linux/amd64 \
		-f docker/smk-runner/v7.Dockerfile \
		--build-arg SNAKEMAKE_VERSION=$(SNAKEMAKE_V7_VER) \
		-t $(REGISTRY)/conquest-smk-$(SNAKEMAKE_V7_VER)-runner:$(TAG) \
		--push .

# Build and push both supported Snakemake runner images.
docker-smk-runner-all:
	$(MAKE) docker-smk-runner TAG=$(TAG)
	$(MAKE) docker-smk-v7-runner TAG=$(TAG)

# Build and push the full image set: server + all runner images.
docker-all:
	$(MAKE) docker-server TAG=$(TAG)
	$(MAKE) docker-nf-runner-all TAG=$(TAG)
	$(MAKE) docker-wdl-runner TAG=$(TAG)
	$(MAKE) docker-smk-runner-all TAG=$(TAG)

# Backward-compatible aliases for the previous target names.
docker-build: docker-server

docker-runner-build: docker-nf-runner

docker-snakemake-runner-build: docker-smk-runner

docker-snakemake-runner-build-v7: docker-smk-v7-runner

docker-snakemake-runner-build-all: docker-smk-runner-all

docker-wdl-runner-build: docker-wdl-runner
