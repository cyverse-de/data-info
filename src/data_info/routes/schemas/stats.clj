(ns data-info.routes.schemas.stats
  (:use [common-swagger-api.schema
         :only [describe
                StandardUserQueryParams]])
  (:require [common-swagger-api.schema.stats :as stats-schema]
            [schema.core :as s]))

(def FileStat stats-schema/FileStat)

(s/defschema StatQueryParams
  (merge StandardUserQueryParams
         stats-schema/StatQueryParams))

(s/defschema FilteredStatQueryParams
  (merge StandardUserQueryParams
         stats-schema/FilteredStatQueryParams))
