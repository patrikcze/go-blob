FROM golang:latest

WORKDIR /app

COPY . .

RUN go build -o go-blob

CMD ["./go-blob"]
