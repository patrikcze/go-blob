FROM golang:latest

WORKDIR /go/src/app
COPY ./release .

RUN go get -d -v ./...
RUN go install -v ./...

CMD ["app"]
