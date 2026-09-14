FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /wallet ./cmd/wallet

FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata && adduser -D wallet
WORKDIR /app
COPY --from=build /wallet /usr/local/bin/wallet
USER wallet
EXPOSE 8080
ENTRYPOINT ["wallet"]
CMD ["serve"]
