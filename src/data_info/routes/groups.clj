(ns data-info.routes.groups
  (:use [common-swagger-api.schema]
        [otel.middleware :only [otel-middleware]]
        [data-info.routes.schemas.users])
  (:require [data-info.services.groups :as groups]
            [schema.core :as s]
            [ring.util.http-response :refer [ok]]))

(s/defschema Group
  {:group-name NonBlankString
   :members [NonBlankString]})

(s/defschema GroupMembers
  (dissoc Group :group-name))

(defroutes groups-routes
  (context "/groups" []
    :tags ["groups"]

    (POST "/" []
      :middleware [otel-middleware]
      :query [params StandardUserQueryParams]
      :body [body Group] ;; need to split out to proper defschema
      :responses (merge CommonResponses
                        {200 {:schema Group
                              :description "Successful response"}})
      :summary "Create group"
      :description (str "Create an IRODS group given a name and a list of members")
      (ok (groups/create-group params body)))

    (context ["/:group-name"] []
      :path-params [group-name :- (describe NonBlankString "The name of an iRODS group, with or without zone qualification")]

      (GET "/" []
        :middleware [otel-middleware]
        :query [params StandardUserQueryParams]
        :responses (merge CommonResponses
                          {200 {:schema Group
                                :description "Successful response"}})
        :summary "List group members"
        :description (str "List an IRODS group & members given a name")
        (ok (groups/get-group params group-name)))

      (PUT "/" []
        :middleware [otel-middleware]
        :query [params StandardUserQueryParams]
        :body [body GroupMembers]
        :responses (merge CommonResponses
                          {200 {:schema Group
                                :description "Successful response"}})
        :summary "Update group members"
        :description (str "Update an IRODS group's members")
        (ok (groups/update-group-members params body group-name)))

      (DELETE "/" []
        :middleware [otel-middleware]
        :query [params StandardUserQueryParams]
        :responses (merge CommonResponses
                          {200 {:schema s/Any ;; probably could be better
                                :description "Successful response"}})
        :summary "Delete group"
        :description (str "Delete an IRODS group's members")
        (ok (groups/delete-group params group-name))))))
