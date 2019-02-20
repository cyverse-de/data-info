(ns data-info.routes.schemas.stats
  (:use [common-swagger-api.schema :only [describe]])
  (:require [common-swagger-api.schema.stats :as stats-schema]
            [schema.core :as s]))

(def DataTypeEnum stats-schema/DataTypeEnum)
(def DataItemIdParam stats-schema/DataItemIdParam)
(def DataItemPathParam stats-schema/DataItemPathParam)
(def StatQueryParams stats-schema/StatQueryParams)
(def FilteredStatQueryParams stats-schema/FilteredStatQueryParams)
(def DataStatInfo stats-schema/DataStatInfo)
(def DirStatInfo stats-schema/DirStatInfo)
(def FileStatInfo stats-schema/FileStatInfo)
(def FilteredStatInfo stats-schema/FilteredStatInfo)
(def AvailableStatFields stats-schema/AvailableStatFields)
(def FileStat stats-schema/FileStat)
(def PathsMap stats-schema/PathsMap)
(def FilteredPathsMap stats-schema/FilteredPathsMap)
(def DataIdsMap stats-schema/DataIdsMap)
(def FilteredDataIdsMap stats-schema/FilteredDataIdsMap)
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
