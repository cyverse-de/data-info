(ns data-info.routes.navigation
  (:use [common-swagger-api.schema]
        [data-info.routes.schemas.common :only [get-error-code-block]])
  (:require [common-swagger-api.schema.data.navigation :as schema]
            [data-info.services.directory :as dir]
            [data-info.services.root :as root]
            [data-info.services.home :as home]
            [data-info.util.service :as svc]))

(defroutes navigation

  (context "/navigation" []
    :tags ["navigation"]

    (GET "/base-paths" [:as {uri :uri}]
      :query [{:keys [user]} StandardUserQueryParams]
      :return schema/UserBasePaths
      :summary "Get User's Base Paths"
      :description (str
"This endpoint returns the base paths of the user's home directory, trash, and the base trash path."
(get-error-code-block
  "ERR_NOT_A_USER"))
      (svc/trap uri root/user-base-paths user))

    (GET "/home" [:as {uri :uri}]
      :query [params StandardUserQueryParams]
      :return schema/RootListing
      :summary "Get User's Home Dir"
      :description (str
"This endpoint returns the ID and path of a user's home directory, creating it if it does not
 already exist."
(get-error-code-block
  "ERR_NOT_A_USER"))
      (svc/trap uri home/do-homedir params))

    (GET "/root" [:as {uri :uri}]
      :query [{:keys [user]} StandardUserQueryParams]
      :return schema/NavigationRootResponse
      :summary "Root Listing"
      :description (str
"This endpoint provides a shortcut for the client to list the top-level directories (e.g. the user's
 home directory, trash, and shared directories)."
(get-error-code-block
  "ERR_DOES_NOT_EXIST, ERR_NOT_READABLE, ERR_NOT_A_USER"))
      (svc/trap uri root/do-root-listing user))

    (GET "/path/:zone/*" [:as {{path :*} :params uri :uri}]
      :path-params [zone :- String]
      :query [params StandardUserQueryParams]
      :return schema/NavigationResponse
      :no-doc true
      (svc/trap uri dir/do-directory zone path params))

    ;; This is actually handled by the above route, which cannot be documented properly.
    (GET "/path/:zone/:path" [:as {uri :uri}]
      :path-params [zone :- (describe String "The IRODS zone")
                    path :- (describe String "The IRODS path under the zone")]
      :query [params StandardUserQueryParams]
      :return schema/NavigationResponse
      :summary "Directory List (Non-Recursive)"
      :description (str
                     "Only lists subdirectories of the directory path passed into it."
                     (get-error-code-block
                       "ERR_DOES_NOT_EXIST, ERR_NOT_READABLE, ERR_NOT_A_USER, ERR_NOT_A_FOLDER"))
      {:status 501})))
