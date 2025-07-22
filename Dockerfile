
FROM reg.mini.dev/mini_dhn4qurcgjcjcvspyujpmzcfzkqdd6js/go

WORKDIR /app
COPY . .
COPY release/go-blob .
RUN go build -o /app/go-blob main.go

CMD ["./go-blob"]
