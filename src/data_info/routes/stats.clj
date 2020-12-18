(ns data-info.routes.stats
  (:use [common-swagger-api.schema]
        [otel.middleware :only [otel-middleware]]
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
      :middleware [otel-middleware]
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
      :middleware [otel-middleware]
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
      (ok (stat/do-stat params body)))))
