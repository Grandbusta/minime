# One person in one container. Build from the repository root:
#   docker build -f deploy/Dockerfile -t minime .
# Their folder (self/) is mounted, never baked in; secrets come from .env.
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/minime ./cmd/minime

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/minime /app/minime
# What every person needs: the config, the prompts, the question sets, and
# the lives they can start from. Paths in the config are relative to /app.
COPY configs ./configs
COPY prompts ./prompts
COPY decisions ./decisions
COPY lives ./lives
# Listen on all addresses inside the container; publish the ports with -p.
# The console then needs CONSOLE_TOKEN (16+ characters) in .env.
ENV MINIME_CONSOLE_ADDR=:8090 MINIME_PROFILE_ADDR=:8080
EXPOSE 8080 8090
ENTRYPOINT ["/app/minime"]
CMD ["run"]
