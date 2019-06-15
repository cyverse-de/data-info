(ns data-info.routes.navigation
  (:use [common-swagger-api.schema]
        [data-info.routes.schemas.common :only [get-error-code-block]]
        [ring.util.http-response :only [ok]])
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
      :responses schema/NavigationRootResponses
      :summary schema/NavigationRootSummary
      :description schema/NavigationRootDocs
      (ok (root/do-root-listing user)))

    (GET "/path/:zone/*" [:as {{path :*} :params uri :uri}]
      :path-params [zone :- String]
      :query [params StandardUserQueryParams]
      :responses schema/NavigationResponses
      :no-doc true
      (ok (dir/do-directory zone path params)))

    ;; This is actually handled by the above route, which cannot be documented properly.
    (GET "/path/:zone/:path" [:as {uri :uri}]
      :path-params [zone :- (describe String "The IRODS zone")
                    path :- (describe String "The IRODS path under the zone")]
      :query [params StandardUserQueryParams]
      :responses schema/NavigationResponses
      :summary schema/NavigationSummary
      :description schema/NavigationDocs
      {:status 501})))
