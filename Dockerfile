FROM archlinux:base AS build
RUN pacman -Sy --noconfirm --needed archlinux-keyring && \
    pacman -Syu --noconfirm go git && \
    pacman -Scc --noconfirm
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /tars ./cmd/tars

FROM archlinux:base
RUN pacman -Sy --noconfirm --needed archlinux-keyring && \
    pacman -Syu --noconfirm ca-certificates tzdata && \
    pacman -Scc --noconfirm
COPY --from=build /tars /usr/local/bin/tars
VOLUME /opt/tars/data
EXPOSE 8899
ENTRYPOINT ["/usr/local/bin/tars"]
CMD ["--config", "/opt/tars/config.yaml"]
