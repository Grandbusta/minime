# One person in one container. Build from the repository root:
#   docker build -f deploy/Dockerfile -t minime .
# Or use the one each release publishes: ghcr.io/grandbusta/minime.
# The person is their database: run it with DATABASE_URL and MINIME_KEY
# (--env-file .env). Nothing about them is in the image, and what every
# person shares is built into the binary.
FROM --platform=$BUILDPLATFORM golang:1.27 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/minime ./cmd/minime

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/minime /minime
# Listen on all addresses inside the container; publish the ports with -p.
# The console then needs their console token (minime new makes one).
ENV MINIME_CONSOLE_ADDR=:8090 MINIME_PROFILE_ADDR=:8080
EXPOSE 8080 8090
ENTRYPOINT ["/minime"]
CMD ["run"]
