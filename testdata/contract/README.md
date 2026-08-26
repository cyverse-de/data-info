# Contract shape-of-record

Captured from the running Clojure `data-info` in the **QA** cluster
(`kubectl -n qa port-forward svc/data-info`), version `3.0.2-SNAPSHOT`, on 2026-08-20.

These files are the reference the Go port is measured against. They are not fixtures the
tests assert on directly — the shadow/diff harness and the golden fixtures do that — but
they pin the published API shape at the moment the port started, so a later disagreement can
be settled without redeploying the Clojure service.

| File | What it is |
|---|---|
| `swagger-clojure.json` | `GET /swagger.json`, pretty-printed with sorted keys so it diffs cleanly. Every path, parameter, and response schema the Clojure service publishes |
| `status-clojure.json` | `GET /` — the status body, including the `iRODS` boolean and the `docs-url` shape |

`docs-url` in `status-clojure.json` reflects the port-forwarded host used to capture it; the
deployed value is derived from the request. Don't treat that one field as literal.

Recapture with:

    kubectl --kubeconfig ~/.kube/qa.conf -n qa port-forward svc/data-info 18080:80 &
    curl -s localhost:18080/swagger.json | python3 -m json.tool --sort-keys \
      > testdata/contract/swagger-clojure.json
