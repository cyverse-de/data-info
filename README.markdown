# data-info

data-info is the Discovery Environment's HTTP API for the iRODS data store:
stat and listing, uploads and downloads, permissions and sharing, tickets,
metadata and AVUs, and the asynchronous move, delete and restore jobs.

This branch holds the Go rewrite. The Clojure service it replaces is on `main`
and stays there until cutover — see `docs/` for what the port preserves
deliberately and what it changes.

## Building and running

```
go build ./cmd/data-info
./data-info --config /etc/iplant/de/data-info.yml
```

`go test ./...` runs the unit and golden-fixture suites; they need no iRODS or
catalog. The integration suites are behind a build tag and refuse to run
without an explicitly scratch-marked root — see `internal/irodsit`.

## Configuration

A YAML file, given with `--config` and defaulting to
`/etc/iplant/de/data-info.yml`. `conf/go/data-info.yml.sample` is a complete,
commented example, and a test asserts it still loads.

Values are resolved as yaml < dotenv < environment, so every setting has a
`DISCOENV_`-prefixed override and no secret has to be written to a file:
`DISCOENV_IRODS_PASSWORD`, `DISCOENV_ICAT_PASSWORD` and `DISCOENV_AMQP_URI`
are the ones that matter.

`GET /admin/config` reports the resolved configuration under the flat
`data-info.*` property names the Clojure service used, with the same masking,
so the two can be diffed.

## API documentation

Swagger UI at `/docs`, and the raw document at `/swagger.json`.

## Shadow harness

`cmd/dishadow` sends the same request to this service and to the Clojure one
and diffs both the responses and the resulting state. Its catalog is in
`test/shadow/catalog/`. It ships in this image, so an in-cluster run exercises
the same binary the build produced.
