FROM golang:1.23-alpine AS build

WORKDIR /app

COPY . ./

RUN go mod download && CGO_ENABLED=0 GOOS=linux go build -o main .

FROM alpine:latest
COPY --from=build /app/main /main

EXPOSE 8080

CMD ["./main"]