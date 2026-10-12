# Where the data-info rewrite stands

Written 2026-10-12. This is the pick-up-from-cold document: what the project is, what is
done, what is blocked, and what to do next.

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

- **`main`** — the Clojure service, still live. Not frozen.
- **`golang`** — the Go rewrite, released as version 4.0.0. The Clojure tree was removed from
  it in #96, so `main` and `golang` are disjoint trees in one repository. `data-info` builds
  from `main`, `data-info-next` from `golang`, each finding a plain `Dockerfile`.
- Changes now land directly on `golang`; review happens on the cutover PR, data-info#106.

Reading the Clojure source from a `golang` checkout is `git show main:src/...`, or a second
worktree. **If a `grep` in `internal/` or `cmd/` returns "No such file or directory", the
checkout is on a `main`-based branch** — that is the shape of the mistake, not an empty
result.

## What is done

**Every operation the Clojure service serves is served here.**
`TestEveryClojureRouteIsServedOrAccountedFor` reports `59 Clojure operations: 57 served,
2 moved to info-typer, 0 not yet ported`, and it runs on every build. `docs/port-gaps.md` is
the account of how the last twelve were found.

**The two operations that moved to info-typer have moved.** `GET /file-types` and
`PUT /data/{data-id}/type` are served by info-typer (info-typer#14, deployments#106) and
terrain asks it for them (terrain#339). All three merged on 2026-08-27, and the released
images contain them: info-typer `v2026.09.01` and terrain `v2026.10.06`. QA runs terrain
`v2026.10.06` and info-typer `v2026.09.01-rc01`, which answers `GET /file-types` (checked
2026-10-12).

**The review on #106 is addressed** (2026-10-12, `f0f2bce..41d0e04`). Every finding was checked
against the Clojure source and its libraries rather than taken on trust, and the fixes make
the port match Clojure rather than adding deviations:

- AVU PUT without `irods-avus` no longer wipes an item's AVUs; `Scope.Close` no longer hangs on
  a queued AVU lookup.
- Metadata-service error responses are relayed verbatim; unchecked errors say `message` on trap
  routes and `reason` on ok routes, as clojure-commons does.
- Sharing makes its ACL changes as the service account; existence is checked as the service
  account wherever Clojure did, so a path the caller cannot see fails its permission check
  instead of being reported missing.
- An untyped file reports `"unknown"`; a restore checks its origin as the service account; a
  task stuck before `running` now times out; notifications go out after the terminal status.
- Smaller ones: DataCite refuses unpairable attributes, metadata export keeps nested AVUs, an
  empty CSV answers 200, the caller's home is not a base path, and a lookup failure is not
  "absent".

Alongside that: the go-irodsclient fork is gone (upstream v0.21.4 carries the fix),
OpenTelemetry is removed, logs are JSON with levels chosen by error code, and the shutdown job
drain is 75s.

## What is blocked, and on what

- **No current image.** QA runs `data-info-next:golang@sha256:44111eae…`, deployed
  2026-08-24 — the image the baseline was recorded against. The descriptor on deployments
  `main` names a later build, `golang@sha256:7809a8b3…` from 2026-09-01, that was never
  deployed. Both predate every change above. Rebuild and redeploy before the next harness run
  or soak.
- **The baseline is stale.** `docs/shadow-baseline.md` (174 matching, 7 differences) was
  recorded on 2026-08-24, before the review fixes changed behaviour on purpose. Six cases were
  added for them and have never run.
- **The deployments `data-info` role still renders Clojure config.** It renders
  `data-info.properties` into `data-info-configs`; this service reads `data-info.yml`. The
  template, manifest and secret key all have to move across. `data-info-next` is the model —
  same template, keyed `data-info.yml` so that adopting it is a rename of the role, not of the
  file.
- **No soak.** The plan asks for the harness green for seven consecutive days before #106
  merges.
- **#106 conflicts with `main` in one file.** `src/data_info/routes/schemas/data.clj` was
  edited on `main` by #98 and deleted on `golang`; the resolution is to keep the deletion.

**One environmental problem is worth resolving before the soak.** iRODS intermittently
answers `ResourceHierarchyException: HIERARCHY_ERROR` when reading a file that was just
written. It hits both services -- the Clojure one threw 92 during a single harness run -- and
re-reading the same path afterwards succeeds every time. During a soak it would look like
intermittent service failure. It is a data-store problem rather than a service one, and its
status has not been checked since August.

## Deploying to QA

`data-info-next` is gated off by default and deliberately absent from `kubernetes.yml`'s
deploy-all list, so it comes up only when asked for by name:

    ansible-playbook -i <qa-inventory> build_it.yml --tags data-info-next
    ansible-playbook -i <qa-inventory> deploy_it.yml --tags data-info-next -e data_info_next_enabled=true

The role's `git_ref` defaults to `golang`, and `-e data_info_next_git_ref=<branch>` builds
another branch. The build rewrites `files/data-info-next.json` and never commits it.
`data_info_next_enabled` is passed on the command line rather than written into the QA
inventory, so nothing else brings it up.

## Open pull requests

| PR              | Base   | What
|-----------------|--------|---------------------------------------------------------------------------
| data-info#106   | `main` | the cutover; merges once the blockers above are cleared
| data-info#102   | `main` | on-demand CI build for the data-info-next image
| deployments#110 | `main` | superseded: `main` already has a newer data-info-next descriptor; close it
| deployments#120 | `main` | drops data-info-next's `OTEL_*` env vars; merge after #106

## The verification harness

`docs/shadow-baseline.md` is the diff-acceptance record the cutover plan calls for -- the
harness deliberately has no expected-difference table, so the matching is a person's job and
that is what they match against. A difference with no entry in it or in
`docs/deferred-fixes.md` is a defect.

Run it **one group at a time** with `scripts/run-group.sh <group> <run-id>`. A whole-catalog
run takes twenty minutes and gets cut short; a group takes two to five. The script sets its
own kubeconfig and refuses a production context, which is not paranoia -- shells here export
`KUBECONFIG` pointing at prod.

Four things the harness cannot catch, learned by it failing to:

- **A registered route that is half implemented.** The download gap passed a route audit.
  A grep for `TODO|not yet|until then|arrives with` over your own tree found it in seconds.
- **A case that never reaches the code it names.** Every sharing case named the caller
  themselves or a nonexistent user, so all of them stopped at a validator. That hid a bug
  where no file could be unshared at all.
- **Anything after the first difference in a case.** It reports one difference per kind, so a
  status trail that diverges early conceals everything later. Four runs said `delete-a-file`
  differs, and each time it was a different, deeper problem.
- **A normalisation that is too greedy.** It does not fail loudly, it invents differences. A
  run id of `c1` was substituted inside uuids and produced nine spurious differences.

It also has a single user, so it cannot exercise anything that needs a second account: sharing
out of a tree the caller does not own, the case the 2026-10-12 sharing fix is for, needs a
manual check in QA.

## What I would do next

1. **Rebuild and redeploy `data-info-next`**, then re-run every harness group and re-record
   `docs/shadow-baseline.md`. Expect fewer differences than seven, not more; any new one with
   no entry is a defect.
2. **Check sharing by hand** with two accounts: share a path out of a collection someone else
   owns.
3. **Port the `data-info` role** to the Go config, manifest and secret key.
4. **Understand the HIERARCHY_ERROR**, or accept that soak noise will include it.
5. **Soak** for seven days, then merge #106, resolving the one conflict by keeping the
   deletion.
6. **Register the upload cleanup as a tracked async task** (deferred fix 20). Small, and the
   only known behavioural gap left that is not a deliberate deviation.

## Traps worth knowing

- **Two preserved bugs**, deliberately: an unrecognised `sort-field` returns a bare 500, and
  `share-count` double-counts users holding two access rows. Both are enforced by the harness
  and recorded in `docs/deferred-fixes.md`. Do not "fix" them.
- **The status table is bug-for-bug.** `ERR_DOES_NOT_EXIST` and `ERR_NOT_READABLE` are 500s.
  `apps` catches `[:status 500]` literally at four sites; changing this breaks job submission.
- **Two Clojure connections, two answers.** Most Clojure validation checks existence over the
  admin connection and access as the caller. Asking both as the caller collapses "you cannot
  see it" into "it does not exist" and changes the error code. Check which one Clojure used
  before writing a validator.
- **`EmittedCodes()` overstates the vocabulary.** `ERR_TOO_MANY_PATHS` appears only in
  Clojure Swagger doc strings and is never thrown by either service. The rest of the list
  deserves the same check.
- **A `preStop` sleep plus 30s server drain plus 75s job drain fit inside the manifest's
  120s grace period.** They are coupled: changing one without the other risks a SIGKILL
  mid-drain, which is what leaves paths locked forever.
