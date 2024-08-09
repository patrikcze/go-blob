# Make variables
APP=go-blob
VERSION=0.1.3
BUILD_TIME=$(shell date -u +%Y-%m-%dT%H:%M:%S.%NZ)
DOCKER_ORG=mytestorg

# Docker image variables
IMAGE=$(DOCKER_ORG)/$(APP)
TAG=$(VERSION)
REGISTRY=hub.docker.com

# Golang binary variables
CGO_ENABLED=0
GOOS=$(shell uname -s | tr A-Z a-z)
GOARCH=$(shell uname -m)

.PHONY: build-app
build-app:
	go build -ldflags "-X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME)" -o release/$(APP) main.go
	cp index.html release/index.html

.PHONY: docker-build
docker-build: build-app
	docker build --pull --build-arg APP=$(APP) --build-arg VERSION=$(VERSION) --build-arg BUILD_TIME=$(BUILD_TIME) --build-arg GOOS=$(GOOS) --build-arg GOARCH=$(GOARCH) -t $(REGISTRY)/$(IMAGE):$(TAG) .
    
.PHONY: start
start:
	docker run --rm --name $(APP) -p 9000:9000 \
	-e AZURE_STORAGE_ACCOUNT_NAME=$$AZURE_STORAGE_ACCOUNT_NAME \
	-e AZURE_STORAGE_ACCOUNT_KEY=$$AZURE_STORAGE_ACCOUNT_KEY \
	-e AZURE_STORAGE_ACCOUNT_CONTAINER=$$AZURE_STORAGE_ACCOUNT_CONTAINER \
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
