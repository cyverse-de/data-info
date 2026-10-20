# Contract shape-of-record

`status-clojure.json` is the body of `GET /` captured from the Clojure `data-info` in the
**QA** cluster, version `3.0.2-SNAPSHOT`, on 2026-08-20. `TestStatusBodyMatchesCapturedShape`
holds this service's status body to the same keys in the same order.

`docs-url` reflects the port-forwarded host used to capture it; the deployed value is derived
from the request. Don't treat that one field as literal.
