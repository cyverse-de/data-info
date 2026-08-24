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

Phases 0 to 11 are merged into `golang`. **Every operation the Clojure service serves is
served here**: `TestEveryClojureRouteIsServedOrAccountedFor` reports `59 Clojure operations:
57 served, 2 moved to info-typer, 0 not yet ported`, and it runs on every build.

The last twelve landed on 2026-08-24 (#105), eleven found by a route audit and one -- file
download on `GET /data/path/{zone}/*` -- that no route-level audit could have found, because
the route was registered and only half implemented. `docs/port-gaps.md` is the account.

`data-info-next` is deployed to QA from the `golang` branch, taking no traffic, and has been
differentially tested against the live Clojure service.

## What is blocked, and on what

**Nothing in the port itself.** The two blockers recorded here previously are both resolved:
the eleven unported endpoints are ported, and the `de-irods` connection limit that held the
QA deployment turned out not to bite -- three concurrent consumers of that account coexist
with no errors on either side. Whether it was ever real or was lifted without anyone noticing
is not something I could determine.

**One environmental problem is worth resolving before the soak.** iRODS intermittently
answers `ResourceHierarchyException: HIERARCHY_ERROR` when reading a file that was just
written. It hits both services -- the Clojure one threw 92 during a single harness run -- and
re-reading the same path afterwards succeeds every time. It makes a fully clean harness run a
matter of luck, and during a soak it would look like intermittent service failure. It is a
data-store problem rather than a service one.

## Deployed to QA

`data-info-next` runs in QA from the `golang` branch, one replica, taking no traffic:

    harbor.cyverse.org/de/data-info-next:golang@sha256:44111eae4601ae1b1d83ae23e611630541de99e8fb6a85c7de31b935e5423e4f

It is gated off by default and deliberately absent from `kubernetes.yml`'s deploy-all list, so
it comes up only when asked for by name:

    ansible-playbook -i <qa-inventory> deploy_it.yml --tags data-info-next -e data_info_next_enabled=true

`data_info_next_enabled` is passed on the command line rather than written into the QA
inventory, so nothing else brings it up. The inventory has an unrelated uncommitted edit in
it — do not commit that.

Rebuild with `build_it.yml --tags data-info-next`; the role's `git_ref` defaults to `golang`,
and `-e data_info_next_git_ref=<branch>` builds a feature branch instead. The build rewrites
`files/data-info-next.json` and never commits it.

## Open pull requests

| PR | Base | What |
|---|---|---|
| data-info#104 | `golang` | this document |
| data-info#102 | `main` | on-demand CI build for the data-info-next image |
| data-info#98 | `main` | drops a dead `heuristomancer` import from the Clojure tree |
| deployments#110 | `main` | points the data-info-next descriptor at the completed port |
| deployments#106 | `main` | info-typer's HTTP endpoint — workstream C |
| info-typer#14 | `main` | info-typer's HTTP API — workstream C |
| terrain#339 | `main` | terrain repointed at info-typer — workstream C |

Workstream C merges in order: info-typer#14 and deployments#106 first, then terrain#339 once
those have deployed. data-info#98 is independent.

## The verification harness

`docs/shadow-baseline.md` is the record: **174 matching cases and 7 differences**, each of the
seven a deliberate deviation entered in `docs/deferred-fixes.md`. That file is the
diff-acceptance record the cutover plan calls for -- the harness deliberately has no
expected-difference table, so the matching is a person's job and that is what they match
against. A difference with no entry is a defect.

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

## What I would do next

1. **Merge the workstream C chain**, in order: info-typer#14, deployments#106, then
   terrain#339. Nothing in data-info depends on it, but the cutover does.
2. **Understand the HIERARCHY_ERROR**, or accept that soak noise will include it.
3. **Soak.** The service is deployed, takes no traffic, and can be pointed at. The plan's
   stage-3 preconditions are otherwise a matter of running the harness repeatedly and
   watching it stay at seven.
4. **Register the upload cleanup as a tracked async task** (deferred fix 20). Small, and the
   only known behavioural gap left that is not a deliberate deviation.

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
