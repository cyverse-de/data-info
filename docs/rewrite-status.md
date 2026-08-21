# Where the data-info rewrite stands

Written 2026-08-21. This is the pick-up-from-cold document: what the project is, what is
done, what is blocked, and what to do next. The full design lives in the plan at
`~/.claude/plans/we-re-looking-into-rewriting-frolicking-dawn.md`; this file is the state.

## The project in a paragraph

`data-info` is the DE's HTTP front end to the iRODS data store — roughly 45 endpoints that
every data operation in the DE funnels through. It is being rewritten from Clojure to Go
because Jargon, the Java iRODS client underneath it, is deprecated and its replacement is
incomplete, while CyVerse maintains `github.com/cyverse/go-irodsclient` and can land fixes
there. The Go service is a **hybrid**: go-irodsclient for writes, ACLs, users and groups;
direct ICAT SQL for the paged listings and bulk per-path reads, which GenQuery cannot
express. The API contract, including its error codes and its wrong-looking HTTP statuses, is
preserved bug-for-bug so that `apps` and `terrain` need no changes.

## The branches

- **`main`** — the Clojure service, still live and still maintained (30 commits in the last
  year). Not frozen.
- **`golang`** — the Go rewrite. The Clojure tree was removed from it in #96, so `main` and
  `golang` are disjoint trees in one repository. `data-info` builds from `main`,
  `data-info-next` from `golang`, each finding a plain `Dockerfile`.
- Work happens on short branches off `golang`, squash-merged, one per phase.

Reading the Clojure source from a `golang` checkout is `git show main:src/...`, or a second
worktree. **If a `grep` in `internal/` or `cmd/` returns "No such file or directory", the
checkout is on a `main`-based branch** — that is the shape of the mistake, not an empty
result.

## What is done

Phases 0–4 and 7–11 are merged to `golang`: the skeleton and the frozen error contract, the
runtime foundation, the iRODS access layer, the ICAT catalog, the request-scoped cache, the
write path, the async machinery and path lock, the mutating endpoints, metadata/AVUs/
path-lists/DataCite/ORE, and the deployment.

Phases 5 and 6 are **partly** done — see the gaps below.

Workstream C (moving file-type detection to info-typer) is written but unmerged.

## What is blocked, and on what

**1. Eleven endpoints were never ported.** See `docs/port-gaps.md` and the `notYetPorted`
list in `cmd/data-info/routes_test.go`. Three of them — manifest, chunks and chunks-tabular —
back file preview in the DE and terrain calls all three, so a cutover today would return 404
for every preview. `/stat-lister` also matters and is not a thin wrapper: it is one of the
two queries that cannot be expressed in GenQuery, so it needs the ICAT `paged-uuid-listing`
path. This is the larger blocker and it is entirely in our hands.

**2. QA iRODS allows `de-irods` one concurrent connection.** Measured 2026-08-20 against
`data.cyverse.rocks`; a second is rejected ~5ms into the handshake, and it is not
Go-specific — six concurrent requests against the *Clojure* service returned five
`ERR_UNAVAILABLE`. `data-info-next` inherits `irods_user: de-irods`, so deploying it would
put a third consumer on a budget the two live replicas already share, with its startup probe
polling every 3s for up to five minutes. **The deploy is deliberately on hold until the
server-side limit is raised.** The soak needs real concurrency regardless: p99 comparison and
the 1000-path bulk cases are meaningless at one connection.

## Ready but not deployed

The `data-info-next` role is merged to the deployments repo's `main` (deployments#107), and
the image is built and pushed:

    harbor.cyverse.org/de/data-info-next:golang@sha256:0ec7e9e1e173a2b4295361aa7217a58e9d1f89b68c787c64b4032bbce2f13d4f

To bring it up once blocker 2 clears: set `data_info_next_enabled: true` in the QA inventory
(`~/work/src/gitlab.cyverse.org/core-sw/qa-deployment/inventory/group_vars/all.yaml`, which
has an unrelated uncommitted edit in it — do not commit that) and run
`deploy_it.yml --tags data-info-next`. It is gated off by default and deliberately absent
from `kubernetes.yml`'s deploy-all list.

## Open pull requests

| PR | Base | What |
|---|---|---|
| data-info#103 | `golang` | the route audit and its permanent guard |
| data-info#100 | `golang` | shadow harness async tier, multi-step cases, error-code coverage |
| data-info#102 | `main` | on-demand CI build for the data-info-next image |
| data-info#98 | `main` | drops a dead `heuristomancer` import from the Clojure tree |
| deployments#106 | `main` | info-typer's HTTP endpoint — workstream C |
| info-typer#14 | `main` | info-typer's HTTP API — workstream C |
| terrain#339 | `main` | terrain repointed at info-typer — workstream C |

Workstream C merges in order: info-typer#14 and deployments#106 first, then terrain#339 once
those have deployed. data-info#98 is independent.

## The verification harness

`cmd/dishadow` sends the same request to both services and diffs the responses **and the
resulting state**. State diffing is what catches the real bugs: a `POST /sharer` can return
an identical response while having set the inherit bit on the wrong collection.

121 cases across 6 groups, in `test/shadow/catalog/`. Three tiers: read (shared fixture),
write (paired `/A/` and `/B/` subtrees, response plus state compared), and async (the same,
plus waiting for the task to carry an end date — the end date rather than the last status,
because an absent end date is what holds a path lock). Cases can carry `before` steps to set
up state, which is what lets restore run after a delete.

It reports which error codes a run elicited. That number is currently **unknown**, because
the harness has never run against both services — it needs blocker 2 cleared. Until then the
"every error code exercised" gate is unmet, not assumed.

## What I would do next

1. **Port manifest and chunking** (`page_tabular.clj`, 165 lines; `manifest.clj`, 60). These
   are user-visible and the largest remaining piece.
2. **Port `/stat-lister`**, then `/creatability-marker` and `/navigation/root`.
3. The five with no known caller last, but do port them — keeping the surface identical was a
   contract decision, and dropping an endpoint should be deliberate.
4. Merge workstream C in its order.
5. Once the iRODS limit is raised: deploy `data-info-next`, run the harness, and let the
   error-code coverage number tell us what the catalog is actually missing.

## Traps worth knowing

- **Two preserved bugs**, deliberately: an unrecognised `sort-field` returns a bare 500, and
  `share-count` double-counts users holding two access rows. Both are enforced by the harness
  and recorded in `docs/deferred-fixes.md`. Do not "fix" them.
- **The status table is bug-for-bug.** `ERR_DOES_NOT_EXIST` and `ERR_NOT_READABLE` are 500s.
  `apps` catches `[:status 500]` literally at four sites; changing this breaks job submission.
- **`EmittedCodes()` overstates the vocabulary.** `ERR_TOO_MANY_PATHS` appears only in
  Clojure Swagger doc strings and is never thrown by either service. The rest of the list
  deserves the same check.
- **A `preStop` sleep plus 30s server drain plus 60s job drain fit inside the manifest's
  120s grace period.** They are coupled: changing one without the other risks a SIGKILL
  mid-drain, which is what leaves paths locked forever.
- `go-irodsclient` is pinned to a fork (`johnworth/go-irodsclient`, branch
  `fix-acquireconnection-mutex-leak`) for a session-mutex leak. Swap back when upstream
  releases the fix.
