(ns data-info.routes.schemas.sharing
  (:use [common-swagger-api.schema :only [describe
                                          NonBlankString]]
        [data-info.routes.schemas.common :only [PermissionEnum]])
  (:require [schema.core :as s]))

(s/defschema AnonFileUrls
  {(describe s/Keyword "the iRODS data item's path")
   (describe NonBlankString "the URL for the file to request in anon-files.")})

(s/defschema AnonShareInfo
  {:user
   (describe NonBlankString "The user performing the request.")

   :paths
   (describe AnonFileUrls "The anon-files URLs for the paths provided with the request.")})

;; Used only for display as documentation in Swagger UI
(s/defschema AnonFilePathsMap
  {:/path/from/request/to/a/file
   (describe NonBlankString "the URL for the file to request in anon-files.")})

;; Used only for display as documentation in Swagger UI
(s/defschema AnonShareResponse
  (assoc AnonShareInfo
         :paths (describe AnonFilePathsMap "The anon-files URLs for the paths provided with the request.")))

(def ShareOutcomeKeys
  {:success
   (describe Boolean "`true` unless the operation failed. A no-op counts as a success.")

   (s/optional-key :reason)
   (describe NonBlankString "Why the operation was a no-op, present only when it was one.")

   (s/optional-key :error)
   (describe s/Any "Details of the failure, present only when the operation failed.")})

(s/defschema PathShareRequest
  {:path       (describe NonBlankString "The path to the file or folder to share")
   :permission (describe PermissionEnum "The permission level to grant to the user")})

(s/defschema PathShareResponse
  (merge PathShareRequest ShareOutcomeKeys))

(s/defschema UserShareRequest
  {:user  (describe NonBlankString "The username to grant permissions to")
   :paths (describe [PathShareRequest] "The paths to share and the permission level to grant on each")})

(s/defschema UserShareResponse
  {:user    (describe NonBlankString "The username permissions were granted to")
   :sharing (describe [PathShareResponse] "The outcome for each requested path")})

(s/defschema SharingRequest
  {:sharing (describe [UserShareRequest] "The sharing requests to process")})

(s/defschema SharingResponse
  {:sharing (describe [UserShareResponse] "The outcome of each sharing request")})

(s/defschema PathUnshareResponse
  (merge {:path (describe NonBlankString "The path to the file or folder being unshared")}
         ShareOutcomeKeys))

(s/defschema UserUnshareRequest
  {:user  (describe NonBlankString "The username to revoke permissions from")
   :paths (describe [NonBlankString] "The paths to the files or folders being unshared")})

(s/defschema UserUnshareResponse
  {:user    (describe NonBlankString "The username permissions were revoked from")
   :unshare (describe [PathUnshareResponse] "The outcome for each requested path")})

(s/defschema UnshareRequest
  {:unshare (describe [UserUnshareRequest] "The unsharing requests to process")})

(s/defschema UnshareResponse
  {:unshare (describe [UserUnshareResponse] "The outcome of each unsharing request")})
