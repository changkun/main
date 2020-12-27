# Copyright 2020 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

NAME=main
VERSION = $(shell git describe --always --tags)
all:
	go build
build:
	CGO_ENABLED=0 GOOS=linux go build
	docker build -t $(NAME):$(VERSION) -t $(NAME):latest .
up:
	docker-compose up -d
down:
	docker-compose down
clean: down
	rm -rf $(NAME)
	docker rmi -f $(shell docker images -f "dangling=true" -q) 2> /dev/null; true
	docker rmi -f $(NAME):latest $(NAME):$(VERSION) 2> /dev/null; true
