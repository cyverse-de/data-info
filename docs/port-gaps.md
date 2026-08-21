# Endpoints the port has not reached

Eleven operations the Clojure service serves are not registered by this one. They are
enumerated in `notYetPorted` in `cmd/data-info/routes_test.go`, each with the caller that
needs it; this file records how they were found and what they mean for the cutover.

## How they were found

The shadow harness gained error-code coverage reporting, which showed that
`ERR_INVALID_PAGE`, `ERR_PAGE_NOT_POS` and `ERR_CHUNK_TOO_SMALL` could not be produced by
this service at all. They are raised only by `page_tabular.clj`, and the endpoints that reach
it had not been written.

That prompted a route-level comparison against `testdata/contract/swagger-clojure.json`, the
Clojure service's own `/swagger.json` captured at phase 0. Nothing had ever compared the two
inventories, which is why the gaps had gone unnoticed through two phase sign-offs.

`TestEveryClojureRouteIsServedOrAccountedFor` is now that comparison, run on every build. A
new gap fails it; a listed gap has to name a reason. Removing an entry as it is ported is the
point of the list.

## What is missing

**Behind file preview in the DE, and the reason a cutover today would be visible to users:**

| Operation | Reached through |
|---|---|
| `GET /data/{data-id}/manifest` | terrain `GET /secured/filesystem/file/manifest` |
| `GET /data/{data-id}/chunks` | terrain `POST /secured/filesystem/read-chunk` |
| `GET /data/{data-id}/chunks-tabular` | terrain `POST /secured/filesystem/read-tabular-chunk` |

terrain relays data-info's status and body verbatim on these, so a swap would surface as a
404 on every preview rather than as anything graceful. `page_tabular.clj` is 165 lines and
`manifest.clj` is 60, so this is the bulk of the remaining work.

**Called by terrain, less visible:**

| Operation | Reached through |
|---|---|
| `POST /creatability-marker` | terrain `can-create-folder` |
| `POST /stat-lister` | terrain's paged stat by uuid |
| `GET /navigation/root` | terrain `list-roots` |

`/stat-lister` is worth singling out: it is one of the two queries the plan identified as
unportable to GenQuery, so it needs the ICAT `paged-uuid-listing` path rather than
go-irodsclient.

**No caller found in terrain, apps, analyses or search:**

`GET /navigation/home`, `GET /data/{data-id}/permissions`, and the three `by-path` forms of
manifest, chunks and chunks-tabular. Absent callers is a reason to port them last, not a
reason to drop them: the contract decision for this port was to keep the API surface
identical, and dropping an endpoint is a decision to take deliberately rather than by
omission.

## Consequence for the cutover

Stage 3's preconditions are not met while any of these are outstanding, and the shadow
harness would have caught them the moment it ran -- every case against them would have
reported a 200-versus-404 difference. Finding them from a route inventory rather than from a
soak is the cheaper order.
