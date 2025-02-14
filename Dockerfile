FROM golang:1.23-alpine AS build

WORKDIR /app

COPY . ./

RUN go mod download && CGO_ENABLED=0 GOOS=linux go build -o main .

FROM alpine:latest

WORKDIR /app

RUN apk update && \
    apk upgrade && \
    apk --no-cache add curl

COPY --from=build /app/main /app/main

EXPOSE 8080

CMD ["./main"]