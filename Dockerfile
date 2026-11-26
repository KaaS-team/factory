# One image for `factory serve`, `factory worker run` and `factory worker plane`.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/factory ./cmd/factory

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/factory /usr/local/bin/factory
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/factory"]
CMD ["--help"]
