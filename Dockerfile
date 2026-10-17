FROM golang:1.26 AS build

WORKDIR /src

# The module graph first, so a source-only change reuses the download layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Static: the runtime image has no libc, and nothing here needs cgo. The version is a constant
# in cmd/data-info/main.go, so there is no -X stamping.
RUN CGO_ENABLED=0 go build \
      -trimpath \
      -ldflags "-s -w" \
      -o /out/data-info \
      ./cmd/data-info

# Distroless: no shell, so less to reach the iRODS and catalog credentials with. Its
# /etc/mime.types disagrees with the DE's types (.vcf is text/vcard, not text/x-vcard), which
# is why internal/mediatype carries its own table.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/data-info /bin/data-info

ENTRYPOINT ["/bin/data-info"]
CMD ["--help"]

ARG git_commit=unknown
ARG version=unknown
ARG descriptive_version=unknown

LABEL org.cyverse.git-ref="$git_commit"
LABEL org.cyverse.version="$version"
LABEL org.cyverse.descriptive-version="$descriptive_version"
LABEL org.label-schema.vcs-ref="$git_commit"
LABEL org.label-schema.vcs-url="https://github.com/cyverse-de/data-info"
LABEL org.label-schema.version="$descriptive_version"
