# Where reads come from: measured, not assumed

The plan splits catalog reads between the iRODS protocol and direct ICAT SQL, on the
theory that go-irodsclient can serve most of them and SQL is needed only for the paged
listings. `cmd/irods-smoke` was written to test that before any endpoint depended on it.

Measured against QA (`data.cyverse.rocks`, **rods4.3.1**) on 2026-08-20, from a workstation,
over a 19-entry collection:

| Operation | Sequential | 4 concurrent |
|---|---|---|
| `Stat`, per path | 9.6 ms | 6.0 ms |
| `ListACLs`, per path | 21.3 ms | 13.8 ms |
| `List` a collection (19 entries, one call) | 24 ms | — |
| `ListACLs` on one collection | 28 ms | — |
| `GetServerVersion` (includes connect and auth) | 262 ms | — |

## What it means

`max_paths_in_request` is 1000, and `POST /path-info` and `POST /permissions-gatherer`
both accept that many paths in one request. Per-path round trips at the rates above give:

- **`/path-info` with 1000 paths: ~6–10 seconds** of stat round trips.
- **`/permissions-gatherer` with 1000 paths: ~14–21 seconds** of ACL round trips.

Today a handful of catalog queries answer the whole set at once, well under a second.

Concurrency does not rescue it. Going from one to four concurrent operations bought about
1.6×, because `FileSystemMetadataConnectionMaxNumberDefault` is **2** — a single session,
and therefore a single client user, gets two metadata connections. Raising it trades
directly against the iRODS server's agent limit: the ceiling this replica can occupy is
sessions × connections-per-session, and the bulk endpoints are exactly where a burst
arrives.

## The correction

**Per-path reads over the iRODS protocol cannot serve the bulk endpoints.** These stay in
ICAT SQL, in addition to the paged listings the plan already assigned there:

- `list-perms-for-item` and the per-user permission queries
- `get-item`, batched across paths rather than issued per path

go-irodsclient keeps the reads where it is genuinely competitive: single-path operations on
the request path, metadata writes, ACL changes, user and group lookups, and everything on
the write path.

## Concurrent connections are scarcer than the config suggests

Discovered while making the integration tests stable, and it constrains the pool directly.

QA **refuses a second concurrent connection for the service account**. Reproducibly: every
operation on one session succeeds, and a health check that opened a session of its own was
rejected with `connection rejected: EOF` on every attempt, including after retries. The
local single-node DE deployment uses the same `de-irods` proxy account against the same
zone, so its connections are part of the same budget.

Two consequences:

- **`maxsessions` x `maxconnections` is a ceiling, not a target.** The defaults (32 x 4)
  describe what the pool will do if the server allows it, and this server plainly will not.
  Whatever those are set to in a deployment has to be checked against what the server will
  actually grant, not chosen on the client's own reasoning.
- **A health check must not open a session of its own.** An earlier version gave the probe a
  dedicated session so that a short probe deadline could not poison the session admin work
  shares. That reasoning was sound and the fix was still wrong here, because the extra
  connection is exactly the resource that is unavailable. The probe now shares the admin
  session, runs on its own generous budget rather than the caller's, and caches its result.

Retrying does not paper over this. A refused connection surfaces inside the first operation
rather than at construction, because sessions connect lazily -- so there is nothing to retry
at the point the session is created, and retrying an arbitrary operation is unsafe when a
failure that looks like a refused connection may be a connection dropped part way through a
write.

## Still worth revisiting

The server is **4.3.1**, which is new enough for GenQuery2. GenQuery2 supports joins,
`ORDER BY` and `LIMIT`/`OFFSET`, so it could express more of the catalog surface than
GenQuery1 — possibly enough to remove the direct database dependency altogether.
go-irodsclient has no GenQuery2 support today. Since CyVerse maintains that library, adding
it is a real option, but it is a separate piece of work and it does not change the
conclusion above: whatever answers a 1000-path request has to answer it in one query.

4.3.1 also means groups live in `r_user_main` as `rodsgroup` rows, which the ported group
queries have to account for.

## Reproducing

    go build -o /tmp/irods-smoke ./cmd/irods-smoke
    /tmp/irods-smoke --config <config> --path /cyverse/home/shared --bulk 19 --parallel 4

It only reads. Nothing it does creates, modifies or deletes anything.
