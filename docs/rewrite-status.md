# Where the data-info rewrite stands

Written 2026-10-15, updated 2026-10-17. This is the pick-up-from-cold document: what the project is, what is
done, what is left, and what to do next.

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

- **`main`** — the Clojure service. No longer what QA runs, and still what prod runs.
- **`golang`** — the Go rewrite, released as version 4.0.0. The Clojure tree was removed from
  it in #96, so `main` and `golang` are disjoint trees in one repository. `main` was merged
  into `golang` on 2026-10-13 keeping that deletion, so #106 merges cleanly.
- Changes land directly on `golang`; review happens on the cutover PR, data-info#106.

Reading the Clojure source from a `golang` checkout is `git show main:src/...`, or a second
worktree. **If a `grep` in `internal/` or `cmd/` returns "No such file or directory", the
checkout is on a `main`-based branch** — that is the shape of the mistake, not an empty
result.

## Where it runs

**QA runs the Go service as `data-info`**, in place of the Clojure one, since 2026-10-15:
image `golang@sha256:730478f6…`, built from e50628c, deployed from the deployments branch
`data-info-golang-cutover` (deployments#122). The UI works against it, including opening
files. `data-info-next`, the side-by-side deployment the harness compared against, is gone
from QA and from the deployments repo.

**A deploy from deployments `main` puts Clojure back.** That happened on 2026-10-16, most
likely as part of a release deploy, and QA was redeployed from the branch the same day. Until
#122 merges, check the image after any QA-wide deploy.

Rolling QA back to Clojure is a redeploy from deployments `main`, which still carries the
Clojure template, manifest and image:

    git switch main
    cd ansible && export KUBECONFIG=$HOME/.kube/qa.conf
    ansible-playbook -i $QA_INVENTORY deploy_it.yml --tags data-info \
        -e kubeconfig=$HOME/.kube/qa.conf -e build_json_dir=$PWD/roles/services/data-info/files

Building from `golang` before #106 merges is `build_it.yml --tags data-info -e
data_info_git_ref=golang`. Pin the QA kubeconfig both ways, as above: the playbooks take it
from `KUBECONFIG`, and shells here export one that points at prod.

## What is done

**Every operation the Clojure service serves is served here.**
`TestEveryClojureRouteIsServedOrAccountedFor` reported `59 Clojure operations: 57 served,
2 moved to info-typer, 0 not yet ported` until it was removed with the Clojure service.
`docs/port-gaps.md` is the account of how the last twelve were found. The two that moved are served by info-typer and
asked for by terrain; all of that is released.

**The harness matched.** The last full run against QA, on 2026-10-13, recorded 182 matching
cases and 9 differences in `docs/shadow-baseline.md`, each a deliberate deviation in
`docs/deferred-fixes.md`. Two-user sharing out of a collection someone else owns was checked
by hand in QA the same day and matched Clojure ACL for ACL, for share and unshare.

**The review on #106 is addressed**, every finding checked against the Clojure source and its
libraries; the fixes make the port match Clojure rather than adding deviations.

**go-irodsclient is upstream v0.21.5**, which carries both fixes made for this service:
cyverse/go-irodsclient#65, a mutex leak, and #66, recursive deletes that failed once they ran
past the operation timeout.

**Found and fixed during the QA cutover:**

- **The Data Store refuses a node's iRODS connections past ten.** Its HAProxy tracks
  connections per client address and the DE nodes are deliberately not exempt, so every pod on
  a node shares that budget, idle connections included. `irods.maxtotalconnections` now caps
  what a replica holds open and makes a call past it wait instead of failing; QA sets it to 6,
  with idle connections closed after a minute. Sixty simultaneous reads all succeed where the
  Clojure service failed 59 of them. `docs/irods-vs-icat-measurements.md` has the numbers.
- **Readiness no longer touches iRODS.** It checks the catalog only, so the probes spend none
  of that budget and an idle replica can drop to no iRODS connections at all.
- **An empty checksum is reported as `"md5": ""`.** It was being omitted, and terrain's response
  schema requires md5 on every file, so opening a file without a checksum failed with a 500.

**Transfers go through the iRODS server data-info connects to**, not straight to the resource
servers. That is how the Clojure service did it too: Jargon's streams never redirected, and
go-irodsclient's redirect-to-resource exists only for its local-file bulk transfers. Checked
on 2026-10-15 and deliberately left as it is.

## What is left before #106 merges

- **Let QA run on it** for as long as seems right. There is no seven-day harness soak: QA is
  the soak.
- **Merge #106, then deployments#122.** Not the other way round: the role builds `main` by
  default, and until #106 merges `main` is Clojure, so a default build would pair the Clojure
  image with the Go config.

**One environmental problem is still there.** iRODS intermittently answers
`ResourceHierarchyException: HIERARCHY_ERROR`, and sometimes `LOCKED_DATA_OBJECT_ACCESS`, when
reading a file that was just written. It hits both services and re-reading succeeds every
time; it was still happening on 2026-10-15. It is a data-store problem rather than a service
one, but it will look like service failure to anyone watching QA closely.

## Open pull requests

| PR              | Base   | What
|-----------------|--------|-------------------------------------------------------
| data-info#106   | `main` | the cutover; merge first
| deployments#122 | `main` | deploys the Go service as `data-info`; merge after #106

data-info#102 and deployments#110, which only concerned `data-info-next`, are closed.

**Review deferred to after the cutover.** slr71's review of 2026-10-16 asked, among smaller
things, to stop answering 500 for every error on the `(ok ...)` routes and the 500 rows of the
status table. That cannot ship on its own: `apps` catches a literal `[:status 500]` from
data-info and switches on `error_code` at four sites (`apps/util.clj`, `jobs/util.clj`,
`de/job_view.clj`, `de/jobs/io_tickets.clj`), and `jobs/util.clj` is what creates a job's
output folder. `apps` has to match on `error_code` first, or in the same release.

## The verification harness

**Removed** once the Clojure service was retired, along with the route guard test; both are
in the history at 99b141e. `docs/shadow-baseline.md` remains as the record of the differences
accepted at cutover.

Five things it could not catch, learned by it failing to, and worth remembering for any
future comparison:

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
- **Data the harness did not create.** Every file it uploads gets a checksum, so it never saw a
  file without one; the md5 bug surfaced only when someone opened an older file in the UI.

## What I would do next

1. **Merge #106 and deployments#122**, in that order, once QA has run on it long enough.
2. **Register the upload cleanup as a tracked async task** (deferred fix 20). Small, and the
   only known behavioural gap left that is not a deliberate deviation.
3. **Stop answering 500 for every error**, together with `apps` (deferred fix 1).

## Traps worth knowing

- **One preserved bug**, deliberately: an unrecognised `sort-field` returns a bare 500
  (deferred fix 3). Do not "fix" it in passing.
- **The status table is bug-for-bug.** `ERR_DOES_NOT_EXIST` and `ERR_NOT_READABLE` are 500s.
  `apps` catches `[:status 500]` literally at four sites; changing this breaks job submission.
- **Two connections, two answers.** Most validation checks existence over the admin
  connection and access as the caller. Asking both as the caller collapses "you cannot see
  it" into "it does not exist" and changes the error code.
- **Terrain validates data-info's responses.** A field the service has always sent, even
  empty, is required by terrain's schema; omitting it is a 500 in the UI, not a harmless
  difference.
- **`EmittedCodes()` overstates the vocabulary.** `ERR_TOO_MANY_PATHS` is never thrown. The
  rest of the list deserves the same check.
- **A `preStop` sleep plus 30s server drain plus 75s job drain fit inside the manifest's
  120s grace period.** They are coupled: changing one without the other risks a SIGKILL
  mid-drain, which leaves the job's paths locked until async-tasks' ten-minute stall timeout.
- **The connection cap is per replica, the Data Store's limit is per node.** Anti-affinity
  keeps the two replicas on separate nodes; raising replicas or `maxtotalconnections` has to
  account for the other iRODS clients on the same node.
