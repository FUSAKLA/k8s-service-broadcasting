
DOCKER_REPO := fusakla
DOCKER_IMAGE_NAME := k8s-service-broadcasting
DOCKER_IMAGE_TAG := $(shell git symbolic-ref -q --short HEAD || git describe --tags --exact-match)

DOCKER_IMAGE := $(DOCKER_REPO)/$(DOCKER_IMAGE_NAME):$(DOCKER_IMAGE_TAG)

all: lint build test

golangci-lint:
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(GOPATH)/bin v1.23.1

lint:
	golangci-lint run  ./...
	go mod tidy

build:
	go build

test:
	go test -race  ./...

docker: build
	docker build -t $(DOCKER_IMAGE) .

docker-publish: docker
	docker login -u $(DOCKER_LOGIN) -p $(DOCKER_PASSWORD) docker.io
	docker push $(DOCKER_IMAGE)
