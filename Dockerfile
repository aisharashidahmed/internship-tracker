# Builds the server (plus the ingest and invite commands) into a small image.
#
#   docker build -t internship-tracker .
#   docker run -p 8080:8080 -e PORT=8080 -e DATABASE_URL=... internship-tracker

FROM golang:1.27-alpine AS build
WORKDIR /src
# Download modules first, so code changes don't re-download them.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Static binaries: the final image has no C library or shell.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ingest ./cmd/ingest \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/invite ./cmd/invite

# distroless: just the binaries, CA certificates (for HTTPS to GitHub and job
# sites), and a non-root user. Nothing else an attacker could use.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ /app/
USER nonroot
EXPOSE 8080
ENTRYPOINT ["/app/server"]
