(ns data-info.routes.sharing
  (:use [common-swagger-api.schema]
        [data-info.routes.schemas.common]
        [data-info.routes.schemas.sharing])
  (:require [data-info.services.sharing :as sharing]
            [data-info.util.service :as svc]))

(defroutes sharing-routes
  (POST "/anonymizer" [:as {uri :uri}]
    :tags ["bulk"]
    :query [params StandardUserQueryParams]
    :body [body (describe Paths "The paths to make readable by the anonymous user.")]
    :return (doc-only AnonShareInfo AnonShareResponse)
    :summary "Make Data Items Anonymously Readable"
    :description (str
"Given a list of files in the body, makes the files readable by the anonymous user."
(get-error-code-block "ERR_NOT_A_FILE, ERR_DOES_NOT_EXIST, ERR_NOT_OWNER, ERR_TOO_MANY_PATHS, ERR_NOT_A_USER"))
    (svc/trap uri sharing/do-anon-files params body))

  (POST "/sharer" [:as {uri :uri}]
    :tags ["bulk"]
    :query [params StandardUserQueryParams]
    :body [body (describe SharingRequest "The paths to share and the users to share them with.")]
    :return SharingResponse
    :summary "Share Data Items"
    :description (str
"Grants users access to the listed paths. Each path is validated and applied on its own, so the"
" outcome is reported per path rather than by failing the request: a path the sharer does not own"
" fails only that entry. Sharing a path with its owner, sharing from the trash, and re-sharing at a"
" permission the user already has are all no-ops, and are reported as successes with a reason. The"
" request is not atomic. Only a failure to connect to iRODS, or a sharer who does not exist, fails"
" the request as a whole."
(get-error-code-block "ERR_NOT_A_USER, ERR_TOO_MANY_RESULTS"))
    (svc/trap uri sharing/do-share params body))

  (POST "/unsharer" [:as {uri :uri}]
    :tags ["bulk"]
    :query [params StandardUserQueryParams]
    :body [body (describe UnshareRequest "The paths to unshare and the users to revoke access from.")]
    :return UnshareResponse
    :summary "Unshare Data Items"
    :description (str
"Revokes users' access to the listed paths, reporting the outcome per path the way /sharer does."
" Revoking access that was never granted, and unsharing a path from its owner, are no-ops reported"
" as successes with a reason."
(get-error-code-block "ERR_NOT_A_USER, ERR_TOO_MANY_RESULTS"))
    (svc/trap uri sharing/do-unshare params body)))
