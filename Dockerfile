FROM alpine:3.5

ENV IMPORT_PATH="github.com/utilitywarehouse/ingress53"

ADD . /go/src/${IMPORT_PATH}

RUN apk add --no-cache \
        -X http://dl-cdn.alpinelinux.org/alpine/edge/community \
        ca-certificates \
        git \
        go \
        glide \
        musl-dev \
  && export GOPATH=/go \
  && cd $GOPATH/src/${IMPORT_PATH} \
  && glide i \
  && CGO_ENABLED=0 go test -v $(glide nv) \
  && CGO_ENABLED=0 go build -v -ldflags '-s -X "main.appGitHash=$(git rev-parse HEAD)" -extldflags "-static"' . \
  && mv "$(basename ${IMPORT_PATH})" / \
  && apk del --no-cache go git glide musl-dev \
  && rm -rf $GOPATH ~/.glide

ENTRYPOINT ["/ingress53"]
