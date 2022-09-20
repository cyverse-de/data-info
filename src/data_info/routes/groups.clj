(ns data-info.routes.groups
  (:use [common-swagger-api.schema]
        [otel.middleware :only [otel-middleware]]
        [data-info.routes.schemas.users])
  (:require [data-info.services.users :as users]
            [schema.core :as s]
            [ring.util.http-response :refer [ok]]))


(defroutes groups-routes
  (context "/groups" []
    :tags ["groups"]

    (POST "/" []
      :middleware [otel-middleware]
      :query [{:keys [user]} StandardUserQueryParams]
      :body [body s/Any] ;; obviously not
      :responses (merge CommonResponses
                        {200 {:schema s/Any
                              :description "Successful response"}})
      :summary "Create group"
      :description (str "Create an IRODS group given a name")
      (ok {}))

    (context ["/:group-name"] []
      :path-params [group-name :- (describe NonBlankString "The name of an iRODS group, with or without zone qualification")]

      (GET "/" []
        :middleware [otel-middleware]
        :query [{:keys [user]} StandardUserQueryParams]
        :body [body s/Any] ;; obviously not
        :responses (merge CommonResponses
                          {200 {:schema s/Any
                                :description "Successful response"}})
        :summary "List group members"
        :description (str "List an IRODS group & members given a name")
        (ok {}))

      (PUT "/" []
        :middleware [otel-middleware]
        :query [{:keys [user]} StandardUserQueryParams]
        :body [body s/Any] ;; obviously not
        :responses (merge CommonResponses
                          {200 {:schema s/Any
                                :description "Successful response"}})
        :summary "Update group members"
        :description (str "Update an IRODS group's members")
        (ok {}))

      (DELETE "/" []
        :middleware [otel-middleware]
        :query [{:keys [user]} StandardUserQueryParams]
        :body [body s/Any] ;; obviously not
        :responses (merge CommonResponses
                          {200 {:schema s/Any
                                :description "Successful response"}})
        :summary "Delete group"
        :description (str "Delete an IRODS group's members")
        (ok {})))))
