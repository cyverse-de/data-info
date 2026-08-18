(ns data-info.routes.schemas.stats
  (:use [common-swagger-api.schema
         :only [describe
                StandardUserQueryParams]])
  (:require [common-swagger-api.schema.data :as data-schema]
            [common-swagger-api.schema.stats :as stats-schema]
            [data-info.routes.schemas.common :refer [->required-param]]
            [schema.core :as s]))

(def FileStat stats-schema/FileStat)

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

(s/defschema DataIdListingParams
  (-> (merge StandardUserQueryParams
             (dissoc stats-schema/FilteredStatQueryParams (s/optional-key :validation-behavior))
             (dissoc data-schema/FolderListingParams (s/optional-key :entity-type))
             ;; this listing always sorts, defaulting to name, so sort-dir applies without sort-field
             {(s/optional-key :sort-dir)
              (describe (s/enum "ASC" "DESC")
                        "Sorts the results in either ascending (`ASC`) or descending (`DESC`) order,
                         before the limit and offset are applied. Defaults to `ASC`.")})
      (->required-param :limit)
      (->required-param :offset)))

(s/defschema DataIdListing
  {:files
   (describe [stats-schema/FilteredStatInfo] "Stat information for the files in this page")

   :folders
   (describe [stats-schema/FilteredStatInfo] "Stat information for the folders in this page")

   :total
   (describe Long "The total number of data items matching the request, ignoring the limit and offset")})
