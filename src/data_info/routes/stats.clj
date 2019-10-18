(ns data-info.routes.stats
  (:use [common-swagger-api.schema]
        [data-info.routes.schemas.common]
        [data-info.routes.schemas.stats])
  (:require [common-swagger-api.schema.data :as data-schema]
            [common-swagger-api.schema.stats :as schema]
            [data-info.services.stat :as stat]
            [data-info.util.service :as svc]))

(defroutes stat-gatherer

            ; FIXME Update apps exception handling when data-info excptn hndlg updated
            ; apps catches exceptions thrown from this EP.
  (context "/stat-gatherer" []
    :tags ["bulk"]

    (POST "/" [:as {uri :uri}]
      :query [params StatQueryParams]
      :body [body data-schema/OptionalPathsOrDataIds]
      :return (doc-only schema/StatusInfo schema/StatResponse)
      :summary "File and Folder Status Information"
      :description (str
"This endpoint allows the caller to get information about many files and folders at once, potentially also "
"validating permissions on the files/folders for the user provided."
(get-error-code-block
  "ERR_DOES_NOT_EXIST, ERR_NOT_READABLE, ERR_NOT_WRITEABLE, ERR_NOT_OWNER, ERR_NOT_A_USER, ERR_TOO_MANY_RESULTS"))
      (svc/trap uri stat/do-stat params body)))

  (context "/path-info" []
    :tags ["bulk"]

    (POST "/" [:as {uri :uri}]
      :query [params FilteredStatQueryParams]
      :body [body data-schema/OptionalPathsOrDataIds]
      :return (doc-only schema/FilteredStatusInfo schema/StatResponse)
      :summary "File and Folder Status Information (allowing filtering)"
      :description (str
"This endpoint allows the caller to get information about many files and folders at once, potentially also validating "
"permissions on the files/folders for the user provided. This endpoint allows specifying includes and excludes to "
"reduce processing needs and/or data size."
(get-error-code-block
  "ERR_DOES_NOT_EXIST, ERR_NOT_READABLE, ERR_NOT_WRITEABLE, ERR_NOT_OWNER, ERR_NOT_A_USER, ERR_TOO_MANY_RESULTS"))
      (svc/trap uri stat/do-stat params body))))
