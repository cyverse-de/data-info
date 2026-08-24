# The shadow harness baseline

What a clean run looks like as of 2026-08-24, and what each remaining difference is. This is
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

The run id must contain a character outside `0-9a-f`. It is substituted wherever it appears,
so an id made only of hex digits lands inside a uuid and an id made only of decimal digits
lands inside a timestamp — `dishadow` refuses those rather than quietly corrupting the values
the normaliser exists to canonicalise.

## Baseline

Against QA on 2026-08-24, candidate at `port-remaining-endpoints`:

| Group | matched | differences | all accounted for |
|---|---|---|---|
| reads | 27 | 0 | — |
| stats | 25 | 0 | — |
| listing | 27 | 0 | — |
| downloads | 5 | 0 (2 skipped) | — |
| mutations | 36 | 0 | — |
| chunking | 28 | 2 | yes |
| uploads | 10 | 3 | yes |
| writes | 16 | 2 | yes |

**174 matched, 7 differences, every one a deliberate deviation recorded in
`docs/deferred-fixes.md`.**

## The seven

| Case | Difference | Entry |
|---|---|---|
| `upload-an-empty-file` | reference 500, candidate 200 | 8 — the reference cannot upload a zero-byte file |
| `upload-a-utf8-name` | reference 400, candidate 200 | 9 — the reference rejects a non-ASCII filename |
| `upload-a-csv` | `infoType` `"csv"` against `""` | 21 — file typing moved to info-typer |
| `manifest-of-a-name-with-no-extension` | `text/plain` against `application/octet-stream` | 11 — content type from the name only |
| `tabular-one-page-past-the-end` | reference 500, candidate 200 | 17 — reading past the end of a short file throws |
| `create-the-zone-root` | reference 500, candidate 400 | 12 — `POST /data/directories` with `"/"` crashes |
| `create-through-a-parent-reference` | reference 500, candidate 200 | 13 — acts on uncleaned paths |

## Cases that cannot be paired

Three are skipped, and the reason is the same each time: fixtures are built **through the
reference**, so that a comparison never depends on the service under test already being
correct — and the reference cannot create these.

- `tabular-empty-file`, `download-an-empty-file` — a zero-byte object (entry 8).
- `download-a-file-whose-name-is-not-ascii` — a non-ASCII filename (entry 9).

Each is covered by a unit test instead, named in the catalog beside the skip.

## Environmental noise, which is not the port

Cases intermittently fail to be compared at all, with the harness reporting an `ERROR` rather
than a difference. Every one seen so far is the same thing: `ResourceHierarchyException:
HIERARCHY_ERROR` from iRODS when **reading a file that was just written**. It hits both
services — the reference threw 92 of them during one run — and re-reading the same path
afterwards succeeds every time.

That is a data-store problem rather than a service one, and it is worth resolving before the
soak: it makes a fully clean run a matter of luck, and during a soak it would look like
intermittent service failure. Note that it is *not* the connection limit that held this
deployment up; three consumers of `de-irods` coexist without trouble.
