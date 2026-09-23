# syntax=docker/dockerfile:1

# ---- build: static binary, no cgo ----
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/pulsecheck .

# ---- run: distroless = no shell, no package manager, CA certs included ----
# Runs as non-root and writes no files, so it works under OpenShift's
# restricted security model, which assigns an arbitrary UID at runtime.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/pulsecheck /pulsecheck
USER nonroot:nonroot
ENTRYPOINT ["/pulsecheck"]
