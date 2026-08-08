FROM golang:1.25-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/containers4agents ./cmd/containers4agents

FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.source="https://github.com/Blue-Door-Software-Group/containers4agents"

COPY --from=build /out/containers4agents /usr/local/bin/containers4agents

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/containers4agents"]
