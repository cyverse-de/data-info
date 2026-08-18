(ns data-info.routes.stats
  (:use [common-swagger-api.schema]
        [data-info.routes.schemas.common]
        [data-info.routes.schemas.stats]
        [ring.util.http-response :only [ok]])
  (:require [common-swagger-api.schema.data :as data-schema]
            [common-swagger-api.schema.stats :as schema]
            [data-info.services.stat :as stat]))

(defroutes stat-gatherer

  ; FIXME Update apps exception handling when data-info excptn hndlg updated
  ; apps catches exceptions thrown from this EP.
  (context "/stat-gatherer" []
    :tags ["bulk"]

    (POST "/" []
      :query [params StatQueryParams]
      :body [body data-schema/OptionalPathsOrDataIds]
      :responses schema/StatResponses
      :summary schema/StatSummary
      :description (str schema/StatDocs
                        " Potentially also validating permissions on the files/folders for the user provided.")
      (ok (stat/do-stat params body))))

  (context "/path-info" []
    :tags ["bulk"]

    (POST "/" []
      :query [params FilteredStatQueryParams]
      :body [body data-schema/OptionalPathsOrDataIds]
      :responses (merge CommonResponses
                        {200 {:schema      (doc-only schema/FilteredStatusInfo schema/StatResponse)
                              :description "File and Folder Filtered Status Response."}
                         500 {:schema      schema/StatErrorResponses
                              :description data-schema/CommonErrorCodeDocs}})
      :summary "File and Folder Status Information (allowing filtering)"
      :description (str "This endpoint allows the caller to get information about many files and folders at once, "
                        "potentially also validating permissions on the files/folders for the user provided. "
                        "This endpoint allows specifying includes and excludes "
                        "to reduce processing needs and/or data size.")
      (ok (stat/do-stat params body))))

  (context "/stat-lister" []
    :tags ["bulk"]

    (POST "/" []
      :query [params DataIdListingParams]
      :body [body data-schema/DataIds]
      :return DataIdListing
      :summary "Paged File and Folder Status Information"
      :description (str "This endpoint returns stat information for a set of data ids, one page at a "
                        "time. The page is selected and ordered by the catalog, so sorting and paging "
                        "apply across the whole set rather than within a response. Entries are the "
                        "same shape /stat-gatherer returns, split into files and folders, alongside "
                        "the total number of matching items.")
      (ok (stat/do-stat-listing params body)))))
