(ns data-info.routes.schemas.stats
  (:use [common-swagger-api.schema
         :only [describe
                StandardUserQueryParams]])
  (:require [common-swagger-api.schema.stats :as stats-schema]
            [schema.core :as s]))

(def FileStat stats-schema/FileStat)
(def FilteredStatInfo stats-schema/FilteredStatInfo)

(s/defschema StatQueryParams
  (merge StandardUserQueryParams
         stats-schema/StatQueryParams))

(s/defschema FilteredStatQueryParams
  (merge StandardUserQueryParams
         stats-schema/FilteredStatQueryParams
         {(s/optional-key :ignore-missing)
          (describe Boolean "If set to true, missing paths or data ids will be ignored.")

          (s/optional-key :ignore-inaccessible)
          (describe Boolean "If set to true, inaccessible paths or data ids will be ignored.")}))
