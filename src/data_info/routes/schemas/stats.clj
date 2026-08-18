(ns data-info.routes.schemas.stats
  (:use [common-swagger-api.schema
         :only [describe
                SortFieldDocs
                StandardUserQueryParams]])
  (:require [common-swagger-api.schema.data :as data-schema]
            [common-swagger-api.schema.filetypes :refer [ValidInfoTypesEnum]]
            [common-swagger-api.schema.stats :as stats-schema]
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
  (merge StandardUserQueryParams
         (dissoc stats-schema/FilteredStatQueryParams (s/optional-key :validation-behavior))
         {:limit
          (describe Long "The maximum number of results to return.")

          :offset
          (describe Long "The number of results to skip before returning any.")

          (s/optional-key :sort-field)
          (describe (apply s/enum data-schema/ValidFolderListingSortFields) SortFieldDocs)

          (s/optional-key :sort-dir)
          (describe (s/enum "ASC" "DESC")
                    "Sorts the results in either ascending (`ASC`) or descending (`DESC`) order,
                     before the limit and offset are applied. Defaults to `ASC`.")

          (s/optional-key :info-type)
          (describe (s/either [ValidInfoTypesEnum] ValidInfoTypesEnum)
                    "A list of info-types with which to filter the result items.")}))

(s/defschema DataIdListing
  {:files
   (describe [stats-schema/FilteredStatInfo] "Stat information for the files in this page")

   :folders
   (describe [stats-schema/FilteredStatInfo] "Stat information for the folders in this page")

   :total
   (describe Long "The total number of data items matching the request, ignoring the limit and offset")})
