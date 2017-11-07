(ns data-info.routes.schemas.path-lists
  (:use [common-swagger-api.schema :only [describe NonBlankString StandardUserQueryParams]]
        [data-info.routes.schemas.common])
  (:require [data-info.util.config :as cfg]
            [schema.core :as s]))

(s/defschema PathListSaveQueryParams
  (merge StandardUserQueryParams
         {:dest
          (describe NonBlankString "An IRODS path to a destination file where the Path List contents will be saved")

          (s/optional-key :path-list-info-type)
          (describe (s/enum (cfg/ht-path-list-info-type)
                            (cfg/multi-input-path-list-info-type))
                    "Specifies the type of Path List to create"
                    :default (cfg/ht-path-list-info-type))

          (s/optional-key :name-pattern)
          (describe NonBlankString
                    "Specifying a regex pattern will filter the results to include
                     only file/folder names that match that pattern")

          (s/optional-key :info-type)
          (describe [NonBlankString]
                    "Specifying one or more info-types will filter the results to include
                     only files with one of those info-types.")

          (s/optional-key :folders-only)
          (describe Boolean
                    "When set to true, then only paths for folders will be included in the results,
                     otherwise only file paths will be included.")

          (s/optional-key :recursive)
          (describe Boolean
                    "When set to true, then paths under all subfolders will also be processed recursively")}))
