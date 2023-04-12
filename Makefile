# Make variables
APP=go-uploader
VERSION=1.0.0
BUILD_TIME=$(shell date -u +%Y-%m-%dT%H:%M:%S.%NZ)
DOCKER_ORG=mytestorg

# Docker image variables
IMAGE=$(DOCKER_ORG)/$(APP)
TAG=$(VERSION)
REGISTRY=registry.$(DOCKER_ORG).com

# Golang binary variables
CGO_ENABLED=0
GOOS=linux
GOARCH=amd64

.PHONY: build
build:
    go build -ldflags "-X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME)" \
    -o release/$(APP) main.go

.PHONY: docker-build
docker-build: build
    docker build --pull \
        --build-arg APP=$(APP) \
        --build-arg VERSION=$(VERSION) \
        --build-arg BUILD_TIME=$(BUILD_TIME) \
        -t $(REGISTRY)/$(IMAGE):$(TAG) .

.PHONY: start
start:
    docker run --rm --name $(APP) -p 9000:9000 \
        -e AZURE_STORAGE_ACCOUNT_NAME=<your_account_name> \
        -e AZURE_STORAGE_ACCOUNT_KEY=<your_account_key> \
        -e AZURE_STORAGE_ACCOUNT_CONTAINER=<your_container_name> \
        $(REGISTRY)/$(IMAGE):$(TAG)

.PHONY: stop
stop:
    docker stop $(APP)

.PHONY: clean
clean:
    rm -rf release

.PHONY: delete
delete:
    docker rmi $(REGISTRY)/$(IMAGE):$(TAG)
