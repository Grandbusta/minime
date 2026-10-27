FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/minime ./cmd/minime

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/minime /minime
COPY configs /configs
ENTRYPOINT ["/minime"]
CMD ["run", "-config", "/configs/minime.yaml"]
