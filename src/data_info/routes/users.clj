(ns data-info.routes.users
  (:use [common-swagger-api.schema]
        [data-info.routes.schemas.common]
        [data-info.routes.schemas.users])
  (:require [data-info.services.users :as users]
            [data-info.util.service :as svc]))


(defroutes users-routes
  (context "/users" []
    :tags ["users"]

    (GET "/:username/groups" [:as {uri :uri}]
      :path-params [username :- (describe String "The username whose groups should be listed")]
      :query [{:keys [user]} StandardUserQueryParams]
      :return UserGroupsReturn
      :summary "Get a user's groups"
      :description (str "Get a list of a user's groups, if the requesting user is allowed to see them"
(get-error-code-block "ERR_NOT_A_USER"))
         (svc/trap uri users/do-list-qualified-user-groups user username))))
