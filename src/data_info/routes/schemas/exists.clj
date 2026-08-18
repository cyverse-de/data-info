(ns data-info.routes.schemas.exists
  (:require [clojure-commons.error-codes :as ce]
            [common-swagger-api.schema
             :refer [describe
                     doc-only
                     CommonResponses
                     ErrorResponseUnchecked]]
            [common-swagger-api.schema.data :as data-schema]
            [schema.core :as s]))

(def CreatabilitySummary "Folder Creatability")
(def CreatabilityDocs
  (str "This endpoint allows the caller to check whether a folder could be created at each of a set of "
       "paths. A path is creatable when the deepest ancestor that already exists is a folder the user "
       "can write to; ancestors that do not exist yet are assumed to be created along with it."))

(s/defschema CreatabilityRequest
  (describe data-schema/Paths "The paths to check for creatability."))

(s/defschema PathCreatabilityMap
  {(describe s/Keyword "The iRODS data item's path")
   (describe Boolean "Whether a folder could be created at this path from the request")})

(s/defschema CreatabilityInfo
  {:paths
   (describe PathCreatabilityMap "Paths creatability mapping")})

;; Used only for display as documentation in Swagger UI
(s/defschema CreatabilityResponsePathsMap
  {(keyword "/path/from/request/to/a/folder")
   (describe Boolean "Whether a folder could be created at this path from the request")})

;; Used only for display as documentation in Swagger UI
(s/defschema CreatabilityResponse
  {:paths
   (describe CreatabilityResponsePathsMap "A map of paths from the request to their creatability info")})

(def CreatabilityErrorCodeResponses
  (conj data-schema/CommonErrorCodeResponses
        ce/ERR_NOT_A_USER
        ce/ERR_TOO_MANY_RESULTS))

(s/defschema CreatabilityErrorResponses
  (merge ErrorResponseUnchecked
         {:error_code (apply s/enum CreatabilityErrorCodeResponses)}))

(s/defschema CreatabilityResponses
  (merge CommonResponses
         {200 {:schema      (doc-only CreatabilityInfo CreatabilityResponse)
               :description "A map of paths from the request to their creatability info"}
          500 {:schema      CreatabilityErrorResponses
               :description data-schema/CommonErrorCodeDocs}}))
