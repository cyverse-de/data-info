# The shadow harness baseline

What a clean run looks like as of 2026-10-12, and what each remaining difference is. This is
the diff-acceptance record the cutover plan asks for: the harness has no expected-difference
table by design, so every difference it reports has to be matched against this file by a
person. A difference with no entry here is a defect.

## Running it

Run **one group at a time**. A whole-catalog run takes twenty minutes and was repeatedly cut
short partway through; a group takes two to five and finishes. Each group needs its own run
id, and the fixtures are reaped per run.

```
scripts/run-group.sh <group> <run-id>     # groups are the basenames in test/shadow/catalog
```

The script sets `KUBECONFIG` itself rather than honouring the ambient one, and refuses a
context whose name looks like production. Neither is paranoia: shells here export
`KUBECONFIG` pointing at prod, so an inherited value aims a harness that creates and deletes
real data at whatever the variable happened to hold. `SHADOW_KUBECONFIG` overrides it, and
has to be set deliberately.

The run id must contain a character outside `0-9a-f`. It is substituted wherever it appears,
so an id made only of hex digits lands inside a uuid and an id made only of decimal digits
lands inside a timestamp — `dishadow` refuses those rather than quietly corrupting the values
the normaliser exists to canonicalise.

## Baseline

Against QA on 2026-10-13, candidate at `golang` c0d4012 (image `golang@sha256:81444c70…`),
reference the Clojure `data-info` `v2026.09.01`. A case counted as noise below is one the
section on environmental noise explains; each has matched in other runs.

| Group     | matched | deliberate | noise | skipped
|-----------|---------|------------|-------|--------
| reads     | 28      | 1          | 0     | 1
| stats     | 24      | 0          | 1     | 0
| listing   | 27      | 0          | 0     | 0
| downloads | 6       | 0          | 0     | 2
| mutations | 42      | 1          | 2     | 0
| chunking  | 29      | 2          | 1     | 1
| uploads   | 10      | 3          | 1     | 0
| writes    | 16      | 2          | 0     | 0

**182 matched and 9 differences, every one a deliberate deviation recorded in
`docs/deferred-fixes.md`.** Four cases were not compared because of iRODS, and one,
`overwrite-a-file`, differed because of a replica the Data Store's rules made; see
"Environmental noise".

The same catalog also matches locally, against both services running on ds-collection's
standalone Data Store (`testing/standalone-env`, iRODS 4.3.1 with the CyVerse rule base) and
a local async-tasks, with none of the read-after-write noise below.

## The deliberate differences

| Case                                   | Difference                                         | Entry
|----------------------------------------|----------------------------------------------------|-----------------------------------------------------------
| `upload-an-empty-file`                 | reference 500, candidate 200                       | 8 — the Clojure service cannot upload a zero-byte file
| `upload-a-utf8-name`                   | reference 400, candidate 200                       | 9 — the Clojure service rejects a non-ASCII filename
| `upload-with-no-extension`             | `text/plain` against `application/octet-stream`   | 11 — content type from the name only
| `manifest-of-a-name-with-no-extension` | `text/plain` against `application/octet-stream`   | 11
| `create-the-zone-root`                 | reference 500, candidate 400                       | 12 — `POST /data/directories` with `"/"` crashes
| `create-through-a-parent-reference`    | reference 500, candidate 200                       | 13 — acts on uncleaned paths
| `tabular-one-page-past-the-end`        | reference 500, candidate 200                       | 17 — reading past the end of a short file throws
| `metadata-csv-with-only-a-header`      | reference 500, candidate 200                       | 23 — a metadata CSV with no rows throws
| `unknown-query-parameter`              | reference 400, candidate 200                       | 25 — unknown query parameters are ignored, not refused

`upload-a-csv` differs in some runs and not others: `infoType` `"csv"` against `"unknown"`
(entry 21). The Clojure service reports whatever type info-typer has set by the time it
answers, and info-typer sets it asynchronously, so the reference's answer depends on timing.

Since the 2026-08-24 baseline, `upload-with-no-extension` differs as entry 11 says it should,
`metadata-csv-with-only-a-header` is new, and the other review-fix cases match.

## Cases that cannot be paired

Three are skipped, and the reason is the same each time: fixtures are built
**through the Clojure service**, so that a comparison never depends on the service under test
already being correct — and the Clojure service cannot create these.

- `tabular-empty-file`, `download-an-empty-file` — a zero-byte object (entry 8).
- `download-a-file-whose-name-is-not-ascii` — a non-ASCII filename (entry 9).

Each is covered by a unit test instead, named in the catalog beside the skip.

A fourth, `service-info`, is skipped by the catalog itself: `GET /` reports build-specific and
connection-dependent values, so the two services can never agree on it.

## Environmental noise, which is not the port

Cases intermittently fail to be compared at all, with the harness reporting an `ERROR`, or
differ on one run and match on the next. Every one seen so far is iRODS **failing to read or
move a data object that was just written**:

- `ResourceHierarchyException: HIERARCHY_ERROR`, which the Clojure service reports as
  `JargonException caught in constructor` and this service logs by name;
- `LOCKED_DATA_OBJECT_ACCESS` on a read;
- `UNIX_FILE_RENAME_ERR (No such file or directory)` when moving a fresh file to the trash.

They hit both services, and the same operation succeeds when it is repeated. On 2026-10-12
they produced 500s from the reference on `stat-lister-one-id`, `tabular-by-path`,
`chunk-starting-past-the-end` and `chunk-splitting-a-multibyte-character`, and from the
candidate on `chunk-from-the-middle` and `chunk-by-path`. None reproduced when probed by hand.

That is a data-store problem rather than a service one, and it is worth resolving before the
soak: it makes a fully clean run a matter of luck, and during a soak it would look like
intermittent service failure. Note that it is *not* the connection limit that held this
deployment up; three consumers of `de-irods` coexist without trouble.

**A replica the rules made.** Clojure's stat reports the size and checksum of a stale replica
after an overwrite, where this service reports the replica that was written. The harness reads
both sides' state back through the reference, so when the Data Store's replication rule has
copied the candidate's seed to a second resource before the overwrite and has not yet copied
the reference's, `overwrite-a-file` shows the candidate at the old size. Reproduced locally
with `irepl`: an overwrite through either service leaves the written replica current and the
other stale (`X`), and both services download the new contents. It is a difference in which
replica Clojure's stat reads, not in what was written.

`restore-a-deleted-file` and `restore-onto-an-occupied-path` named the original path rather
than the trash path until 2026-10-12, so neither had restored anything since August. Both now
capture the trash path from their delete and match locally. In QA, `restore-a-deleted-file`
matched on 2026-10-13; `restore-onto-an-occupied-path` has still not been compared there,
because the reference could not read back its state.
