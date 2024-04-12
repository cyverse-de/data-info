(ns data-info.routes.path-lists
  (:use [common-swagger-api.schema]
        [otel.middleware :only [otel-middleware]]
        [data-info.routes.schemas.common]
        [data-info.routes.schemas.path-lists]
        [data-info.routes.schemas.stats])
  (:require [data-info.services.path-lists :as path-list-svc]
            [data-info.util.service :as svc]))

(defroutes path-list-creator
  (context "/path-list-creator" []
           :tags ["bulk"]

    (POST "/" [:as {uri :uri}]
          :middleware [otel-middleware]
          :query [params PathListSaveQueryParams]
          :body [body (describe Paths "The folder or file paths to process for the HT Path List file contents.")]
          :responses {200 {:schema      FileStat
                           :description "File info for the saved HT Path List file."}}
          :summary "Create an HT Path List File"
          :description
          (str
            "Generates an HT Path List file for all paths under the given set of folder paths,
             saving the results to the given destination file.
             The paths for folders given in the request will not be included in the results,
             but if any file paths are included in the request,
             those paths will also be processed according to the other given parameters."
            (get-error-code-block
              "ERR_NOT_FOUND, ERR_EXISTS, ERR_DOES_NOT_EXIST, ERR_NOT_READABLE, ERR_NOT_WRITEABLE, ERR_NOT_A_USER,"
              "ERR_BAD_PATH_LENGTH, ERR_BAD_DIRNAME_LENGTH, ERR_BAD_BASENAME_LENGTH"))
          (svc/trap uri path-list-svc/create-path-list params body))))
