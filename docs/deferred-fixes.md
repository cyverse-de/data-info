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

The upload routes are a third case. `POST /data` and `PUT /data/{data-id}` *are* wrapped in
`svc/trap`, but every error they can raise comes from `write/wrap-multipart-create` and
`write/wrap-multipart-overwrite` — ring middleware that stores the file, and that sits
outside the trap. So those errors reach the default handler too: a forbidden filename answers
500 where the table says `ERR_BAD_OR_MISSING_FIELD` is 400. Verified against the running QA
service.

Reproduced by `apierror.Style`; `StyleOK` is registered on `/existence-marker`,
`/creatability-marker`, the `/groups` routes, `GET /navigation/root`,
`GET /navigation/path/{zone}/*`, `/stat-gatherer`, `/path-info`, `/stat-lister`,
`/tickets`, `/ticket-lister`, `/ticket-deleter`, `POST /data` and `PUT /data/{data-id}`.

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

## 6. `PUT /data/{data-id}` validates read access, not write

`overwrite-path` in `services/write.clj` runs `[:path-readable path user zone]` where its own
docstring says "the user can write to it". A user with read-only access therefore gets past
the check and is refused by iRODS instead, which surfaces as `ERR_UNAVAILABLE` rather than
`ERR_NOT_WRITEABLE`.

Reproduced in `internal/handlers/upload.go` `Overwrite`.

**Blocked on:** nothing structural, but it changes which error a read-only caller sees, so it
wants its own change. Nothing is unsafe in the meantime — iRODS enforces the permission
whatever this check says.

## 7. The upload routes report a missing path as `null`

`POST /data` without a `dest`, and `PUT /data/{data-id}` with an id that resolves to nothing,
both end at a `:path-exists` check on a nil path. The result is
`{"error_code":"ERR_DOES_NOT_EXIST","paths":[null]}` with a 500 — an error that names no
path, because there is none. The same happens to the caller: no `user` parameter gives
`{"error_code":"ERR_NOT_A_USER","users":[null]}`.

The cause is ordering. The multipart middleware that does the work reads `user` and `dest`
straight off the raw request and runs before compojure-api coerces the route's parameters, so
the required-parameter check never gets to reject anything.

Reproduced in `internal/handlers/upload.go`; verified against the running QA service.

**Blocked on:** nothing structural. Reporting the id, or rejecting the missing parameter with
a 400, are both improvements — but they change bodies terrain parses, so they want their own
change.

## 8. A zero-byte upload fails

`POST /data` with an empty file answers `ERR_UNCHECKED_EXCEPTION` with a 500: `get-info-type`
runs heuristomancer over the stream before anything is written, and it does not survive an
empty one.

**Not reproduced.** The Go service has no file-type detection on this path at all — that work
belongs to info-typer now — so an empty upload simply succeeds. Nothing would be gained by
reproducing a crash, and callers cannot be relying on one.

## 9. A non-ASCII filename is rejected

`POST /data` with a UTF-8 filename answers `ERR_ILLEGAL_ARGUMENT` with a 400, from
compojure-api's coercion of the multipart parameters rather than from any deliberate check.

**Not reproduced, by decision.** Go's multipart reader decodes the name and the upload
succeeds. This is the one entry on this list that *widens* what the service accepts, and it
was signed off as an improvement rather than a regression to reproduce: the reference's 400
is an accident of its coercion layer, not a rule anybody wrote. The shadow case
`upload-a-utf8-name` will keep reporting the difference until the Clojure service is gone,
which is the intended outcome, not a defect to chase.

## 10. Tika names some file types wrongly, and the port keeps those names

`internal/mediatype`'s table was measured against the running QA service rather than
guessed — every entry was checked by uploading a sample and reading back the `content-type`.
Several of the answers are wrong on their face and are reproduced anyway:

| extension | reported as | what it actually is |
|---|---|---|
| `.vcf` | `text/x-vcard` | a variant call file, not a contact card |
| `.r` | `text/x-rsrc` | fine, but not a type anything else uses |
| `.log` | `text/x-log` | non-standard |
| `.yml`, `.yaml` | `text/x-yaml` | superseded by `application/yaml` |
| `.ipynb` | `text/plain` | JSON |

**Blocked on:** nothing structural, but these are values callers can switch on, so correcting
them is a change to the API rather than to this service's internals. Do it with the consumers
in view, not during the port.

## 11. Content type is read from the name only

The reference detects from the name and then, whenever the name gives
`application/octet-stream` or `text/plain`, **opens the data object and reads its contents**
(`util/irods.clj` `detect-media-type`). `services/stat.clj` calls that for every non-directory
path whenever `content-type` is among the requested fields, which is the default — so a
`/path-info` over the thousand-path limit opens up to a thousand objects.

**Not reproduced,** deliberately: that is the round-trip-per-path cost the whole hybrid split
exists to avoid (plan risk 12). The table closes the gap for every type whose name Tika finds
decisive. What is left is a file whose name says nothing — most visibly one with no extension
at all, which the reference reads and reports as `text/plain` where this reports
`application/octet-stream`. Pinned by the shadow case `upload-with-no-extension`.

The one place sniffing is free is the download path, where the stream is already open and the
reference sniffs from it too (`services/entry.clj` `file-entry`). When that endpoint lands,
detect there from the open stream rather than reaching back into the catalog.

## 12. `POST /data/directories` with `"/"` crashes

A path of `/` passes the non-blank check and then fails inside `ft/dirname`, giving
`ERR_UNCHECKED_EXCEPTION` with a 500 and the message "both parent and child names are empty".

**Not reproduced.** `internal/handlers/create.go` refuses it as a schema failure with a 400
alongside blank entries, because the alternative is either a 200 for a request that created
nothing or matching a JVM exception message. Pinned by the shadow case
`create-the-zone-root`, which is expected to differ.

## 13. `POST /data/directories` acts on uncleaned paths

The reference passes a requested path to iRODS exactly as written, so
`/zone/home/me/a/../b` is checked and created as that literal string; in practice iRODS
refuses it and the caller gets `ERR_NOT_WRITEABLE` with a 500.

**Not reproduced.** `internal/handlers/create.go` canonicalises first, so the request creates
`/zone/home/me/b` and answers 200. This is a correctness fix rather than a liberty:
go-irodsclient cleans a path before acting on it, so validating the uncleaned form would
check writability on one collection and create under another — the permission check would
pass against the caller's own home while the collection appeared elsewhere. Cleaning makes
the path that is validated the path that is created.

Pinned by the shadow case `create-through-a-parent-reference`, which is expected to differ.

## 14. A task that never reports leaves its paths locked forever

There is no lock table. A path is locked when some unfinished async task names it, and a task
is unfinished exactly when its `end_date` is null — `validate-unlocked` asks async-tasks for
tasks with `include_null_end=true` and an end date after the year 9999, which is how it
spells "everything still running".

The `statuschangetimeout` behaviour looks like the safety net for a task whose process died,
and it is not. Its processor
(`async-tasks/behaviors/statuschangetimeout/statuschangetimeout.go`) calls `CompleteTask` —
the only thing that sets `end_date` — **only when the behaviour's data carries
`complete: true`**. Neither of the two places that register it does:

- `services/rename.clj:106`
- `services/write.clj:131`

Both send `{"statuses":[{"start_status":"running","end_status":"detected-stalled","timeout":"10m"}]}`.

So the timeout adds a `detected-stalled` status and stops. **A pod killed mid-move locks
those paths permanently**, not for ten minutes, and nothing releases them but a person.
(Checked against QA: 106 data-* tasks, none with a null end date, so nothing is stuck there
right now — the mechanism is what is wrong, not the current state.)

**What this port does about it.** `internal/worker` guarantees that a job which starts
reaches a terminal status: the terminal post is made with `context.WithoutCancel` and retried
a hundred times, and `Runner.Shutdown` cancels each running job, waits for it, and lets it
record itself as failed — which sets the end date and releases its paths. That turns a
rollout from a source of permanent locks into a non-event.

**Decided, and done:** every task this service creates registers the behaviour with
`"complete": true` (`asynctasks.StallBehavior`), so the ten-minute timeout completes the task
and releases its paths. That covers what the drain cannot — SIGKILL, OOM, a node dying. The
trade, accepted knowingly: a job that genuinely goes ten minutes without posting a status has
its paths released while it is still working. Jobs here report per path per step, so that
window belongs to a process that is gone rather than one that is busy.

The Clojure service still omits the flag, so until cutover a task it creates behaves the old
way. That is a one-line change in `services/rename.clj` and `services/write.clj` if the
benefit is wanted before then; it is not required for the port.

**Consequence for the cutover:** the drain gate in stage 3 is a hard requirement, not a
nicety. Any in-flight move, rename, delete or restore at the swap will lock its paths
permanently if its pod goes away.

**Also needed, and it belongs to the deployments repo:** nothing sets
`terminationGracePeriodSeconds` for this service, so it is Kubernetes' default of 30 seconds.
The drain has to fit inside that alongside the listener's own shutdown, which is why
`serverGrace` and `drainGrace` in `cmd/data-info/main.go` are 10s and 15s rather than
anything comfortable. Raising the grace period to 120s — which the plan already calls for at
stage 0, along with a `preStop` sleep — lets both grow, and gives a job under way a real
chance to report rather than a hurried one.

## 15. The lock's prefix test does not respect component boundaries

`validate-unlocked` decides that two paths collide with

```clojure
(some #(string/starts-with? (ft/add-trailing-slash %) path) locked-paths)
(some #(string/starts-with? (ft/add-trailing-slash path) %) locked-paths)
```

Each compares one path against another plus a slash without requiring the match to end on a
component boundary, so a locked `/a/b` collides with `/ab` and a locked `/a` collides with
`/ab/c`. Neither is inside the other; the request is refused with `ERR_CONFLICT` for no
reason. (The comments beside those clauses also describe them backwards — `starts-with? a b`
asks whether *a* begins with *b*, so clause one finds ancestors of the locked path, not
descendants.)

**Not reproduced.** `internal/locks` uses `a == b || a starts with b+"/" || b starts with
a+"/"`, which is what the comments meant. It is strictly more permissive than the reference
— it can only allow pairs the reference refused spuriously, never allow one it refused for a
real reason — but it is a behaviour change and is signed off as one here rather than slipped
in. `TestConflicts` pins both the cases that must still collide and the ones that must not.

