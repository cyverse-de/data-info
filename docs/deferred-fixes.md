# Deferred fixes

Known-wrong behavior that the Go port reproduces **on purpose**, so that the rewrite is a
pure behavior-preserving move and any difference the shadow/diff harness reports is a real
defect.

Do not "fix" anything on this list as a drive-by. Each entry names what has to change first.

## 1. Error codes map onto the wrong HTTP statuses

`ERR_DOES_NOT_EXIST` answers 500 rather than 404, `ERR_NOT_READABLE` and
`ERR_NOT_WRITEABLE` answer 500 rather than 403, and most other codes fall to a 500 default.
Reproduced in `internal/apierror/status.go`.

**Blocked on:** `apps` branches on the literal status 500 in four places, each of which
breaks the day data-info answers 404 or 403:

- `apps/src/apps/service/apps/jobs/util.clj` `create-output-dir` — job submission into a
  new output folder depends on it, and already carries a `FIXME` about this
- `apps/src/apps/service/apps/util.clj` `paths-accessible?`
- `apps/src/apps/service/apps/de/job_view.clj` `validate-hidden-inputs`
- `apps/src/apps/service/apps/de/jobs/io_tickets.clj` `delete-tickets`

**Order to fix:** make `apps` status-agnostic first — one helper that catches any numeric
`:status` and dispatches on `error_code`, which works against both the old and new regimes —
soak that in QA, then change the table here. Terrain needs no change either way; it relays
status and body verbatim and keys on `error_code`.

## 2. Two error paths answer differently for the same code

Routes written as `(ok ...)` in the Clojure service let thrown `{:error_code ...}` maps
escape to `clojure-commons.exception`'s `::ex/default` handler, which answers 500
unconditionally. Routes wrapped in `svc/trap` use the status table instead. So
`ERR_NOT_OWNER` is 403 on `POST /deleter` and 500 on `POST /path-info`.

Reproduced by `apierror.Style`; `StyleOK` is registered on `/existence-marker`,
`/creatability-marker`, the `/groups` routes, `GET /navigation/root`,
`GET /navigation/path/{zone}/*`, `/stat-gatherer`, `/path-info`, `/stat-lister`,
`/tickets`, `/ticket-lister` and `/ticket-deleter`.

**Blocked on:** the same `apps` work as entry 1. Once statuses are corrected this
distinction should collapse — every route should answer from one table.

## 3. An unrecognized `sort-field` answers 500

`resolve-sort-column` throws a bare exception rather than a validation error, so a typo in
`sort-field` produces a 500 where `ERR_BAD_QUERY_PARAMETER` / 400 is meant.

**Blocked on:** nothing structural — it is on this list only to keep the port diff-clean.
Fix it in the same change as entry 1.

## 4. `share-count` double-counts

`mk-perms-for-item` selects `(object_id, user_name, access_type_id)` with no `DISTINCT` or
`MAX`, so a user holding two access rows is counted twice and the share count shown in the
UI is inflated.

**Blocked on:** nothing structural. The fix is `DISTINCT ON (user_name) ... ORDER BY
user_name, access_type_id DESC`. It changes a number users see, so it wants its own change
and its own note, not a silent correction during the port.

## 5. `GET /admin/config` does not mask the AMQP URI

The mask is `(?:irods|icat)[-.](?:user|pass)`, so `data-info.amqp.uri` — which embeds the
broker password — is returned in the clear.

**Blocked on:** nothing. Broadening the mask to `(?i)pass|secret|token|password` is safe on
its own; it is listed here only so the port reproduces today's output while
`GET /admin/config` is being diffed against the Clojure service as a config-translation
check (all 52 keys at once). Fix immediately after that check passes.
