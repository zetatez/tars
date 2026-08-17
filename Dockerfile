FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /tars ./cmd/tars

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /tars /usr/local/bin/tars
VOLUME /opt/tars/data
EXPOSE 8899
ENTRYPOINT ["/usr/local/bin/tars"]
CMD ["--config", "/opt/tars/config.yaml"]
