(ns data-info.routes.schemas.navigation
  (:use [common-swagger-api.schema :only [describe]]
        [data-info.routes.schemas.stats])
  (:require [schema.core :as s]))

(s/defschema UserBasePaths
  {:user_home_path  (describe String "The absolute path to the user's home folder")
   :user_trash_path (describe String "The absolute path to the user's trash folder")
   :base_trash_path (describe String "The absolute path to the base trash folder")})

(s/defschema RootListing
  (dissoc DataStatInfo :type))

(s/defschema NavigationRootResponse
  {:roots      [RootListing]
   :base-paths UserBasePaths})

(s/defschema FolderListing
  (-> DataStatInfo
      (dissoc :type)
      (assoc (s/optional-key :folders)
             (describe [(s/recursive #'FolderListing)] "Subdirectories of this directory"))))

(s/defschema NavigationResponse
  {:folder FolderListing})
