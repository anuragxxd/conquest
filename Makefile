.PHONY: build test lint fmt vet clean docker-build docker-runner-build docker-snakemake-runner-build

GO := go
MODULE := github.com/anuragxxd/conquest
REGISTRY := harbor.pacificanalytics.com/pacificanalytics
TAG ?= latest
NXF_VER ?= 24.10.4
NXF_PREINSTALL_PLUGINS ?= nf-ga4gh@1.3.0 nf-amazon@2.9.0
NXF_PLUGINS_ALLOWED ?= nf-ga4gh,nf-amazon,nf-schema,nf-core-utils,nf-fgbio,nf-prov
SNAKEMAKE_VER ?= 9.17.3

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
docker-build:
	docker buildx build --platform linux/amd64 \
		-t $(REGISTRY)/conquest:$(TAG) \
		--push .

# Build and push the conquest runner image (Nextflow + nf-ga4gh + conquest).
docker-runner-build:
	docker buildx build --platform linux/amd64 \
		-f Dockerfile.runner \
		--build-arg NXF_VER=$(NXF_VER) \
		--build-arg NXF_PREINSTALL_PLUGINS="$(NXF_PREINSTALL_PLUGINS)" \
		--build-arg NXF_PLUGINS_ALLOWED=$(NXF_PLUGINS_ALLOWED) \
		-t $(REGISTRY)/conquest-runner:$(TAG) \
		--push .

# Build and push the conquest Snakemake runner image.
docker-snakemake-runner-build:
	docker buildx build --platform linux/amd64 \
		-f Dockerfile.snakemake-runner \
		--build-arg SNAKEMAKE_VERSION=$(SNAKEMAKE_VER) \
		-t $(REGISTRY)/conquest-snakemake-runner:$(TAG) \
		--push .
