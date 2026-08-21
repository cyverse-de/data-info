# The service's image. The Clojure tree lives on main until cutover and builds its own image
# from the Dockerfile there, so the two never need to coexist in one checkout: data-info
# builds from main and data-info-next from this branch, each from the plain name.
FROM golang:1.26 AS build

WORKDIR /src

# The module graph first, so a source-only change reuses the download layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Statically linked, because the runtime image has no libc to link against. Nothing here
# needs cgo: the iRODS client and the postgres driver are both pure Go.
#
# No -X stamping of the version. The service reports a constant it holds in source, the way
# project.clj declares the Clojure service's, so the build needs no arguments to produce a
# binary that identifies itself -- and none of the DE's build paths pass any.
RUN CGO_ENABLED=0 go build \
      -trimpath \
      -ldflags "-s -w" \
      -o /out/data-info \
      ./cmd/data-info

# Distroless rather than a shell image. There is nothing to exec into, which is the point:
# this service holds iRODS and catalog credentials, and the smaller the runtime the less
# there is to reach them with.
#
# Worth knowing for anyone debugging it: this image does ship an /etc/mime.types, Debian's,
# and its answers are not the ones the DE has always given -- it calls .vcf text/vcard where
# data-info reports text/x-vcard. That is why internal/mediatype carries its own table rather
# than letting the standard library's mime package seed itself from that file.
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
