DOCKER_IMAGE_NAME = modokipaas/modoki-ssh-gateway

SRCS = $(wildcard *.go)

.PHONY: all build
all:  docker

docker: $(SRCS) Dockerfile
	docker build -t $(DOCKER_IMAGE_NAME) .

