# Copyright (c) 2018 tsuzu
# 
# This software is released under the MIT License.
# https://opensource.org/licenses/MIT

FROM golang:1.10-alpine as build

RUN apk add --no-cache git
RUN go get -v github.com/modoki-paas/modoki-ssh-gateway

WORKDIR /go/src/github.com/modoki-paas/modoki-ssh-gateway

COPY . /go/src/github.com/modoki-paas/modoki-ssh-gateway
RUN go get -v .
RUN CGO_ENABLED=0 go build -o /bin/modoki-ssh-gateway

FROM scratch
COPY --from=build /bin/modoki-ssh-gateway /bin/modoki-ssh-gateway
ENTRYPOINT ["/bin/modoki-ssh-gateway"]
CMD ["--help"]