
FROM golang:latest

WORKDIR /app
COPY . .
COPY release/go-blob .
RUN go build -o /app/go-blob main.go

CMD ["./go-blob"]
