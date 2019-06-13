(ns data-info.routes.schemas.stats
  (:use [common-swagger-api.schema :only [describe]])
  (:require [common-swagger-api.schema.stats :as stats-schema]
            [schema.core :as s]))

(def StatQueryParams stats-schema/StatQueryParams)
(def FilteredStatQueryParams stats-schema/FilteredStatQueryParams)
(def DirStatInfo stats-schema/DirStatInfo)
(def FileStatInfo stats-schema/FileStatInfo)
(def AvailableStatFields stats-schema/AvailableStatFields)
(def FileStat stats-schema/FileStat)
(def StatusInfo stats-schema/StatusInfo)
(def FilteredStatusInfo stats-schema/FilteredStatusInfo)

;; Used only for display as documentation in Swagger UI
(s/defschema StatResponsePathsMap
  {:/path/from/request/to/a/folder (describe DirStatInfo "A folder's info")
   :/path/from/request/to/a/file   (describe FileStatInfo "A file's info")})

;; Used only for display as documentation in Swagger UI
(s/defschema StatResponseIdsMap
  {:some-folder-uuid (describe DirStatInfo "A folder's info")
   :some-file-uuid   (describe FileStatInfo "A file's info")})

;; Used only for display as documentation in Swagger UI
(s/defschema StatResponse
  {(s/optional-key :paths) (describe StatResponsePathsMap "A map of paths from the request to their status info")
   (s/optional-key :ids) (describe StatResponseIdsMap "A map of ids from the request to their status info")})
