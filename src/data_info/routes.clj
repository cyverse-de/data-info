(ns data-info.routes
  (:use [clojure-commons.lcase-params :only [wrap-lcase-params]]
        [clojure-commons.query-params :only [wrap-query-params]]
        [common-swagger-api.schema]
        [compojure.api.middleware :only [wrap-exceptions]]
        [service-logging.middleware :only [log-validation-errors add-user-to-context]]
        [ring.util.response :only [redirect]])
  (:require [compojure.route :as route]
            [clojure-commons.exception :as cx]
            [data-info.routes.avus :as avus-routes]
            [data-info.routes.data :as data-routes]
            [data-info.routes.exists :as exists-routes]
            [data-info.routes.filetypes :as filetypes-routes]
            [data-info.routes.groups :as groups-routes]
            [data-info.routes.path-lists :as path-lists]
            [data-info.routes.permissions :as permission-routes]
            [data-info.routes.navigation :as navigation-routes]
            [data-info.routes.rename :as rename-routes]
            [data-info.routes.sharing :as sharing-routes]
            [data-info.routes.status :as status-routes]
            [data-info.routes.stats :as stat-routes]
            [data-info.routes.tickets :as ticket-routes]
            [data-info.routes.trash :as trash-routes]
            [data-info.routes.users :as users-routes]
            [data-info.util :as util]
            [data-info.util.config :as config]
            [data-info.util.service :as svc]
            [ring.middleware.keyword-params :as params]))

(defapi app
  (swagger-routes
   {:ui      config/docs-uri
    :options {:ui {:supported-submit-methods ["get", "post", "put", "delete", "patch", "head"]
                   :validator-url            nil}}
    :data    {:info {:title       "Discovery Environment Data Info API"
                     :description "Documentation for the Discovery Environment Data Info REST API"
                     :version     "2.8.0"}
              :tags [{:name "service-info", :description "Service Information"}
                     {:name "data-by-id", :description "Data Operations (by ID)"}
                     {:name "data", :description "Data Operations"}
                     {:name "tickets", :description "Ticket Operations"}
                     {:name "bulk", :description "Bulk Operations"}
                     {:name "navigation", :description "Navigation"}
                     {:name "users", :description "User Operations"}
                     {:name "filetypes", :description "File Type Metadata"}
                     {:name "groups", :description "Group Operations"}]}})
  (context "/" []
    :middleware [add-user-to-context
                 wrap-query-params
                 wrap-lcase-params
                 params/wrap-keyword-params
                 [wrap-exceptions cx/exception-handlers]
                 util/req-logger
                 log-validation-errors]
    status-routes/status
    data-routes/data-operations
    rename-routes/rename-routes
    avus-routes/avus-routes
    exists-routes/existence-marker
    filetypes-routes/filetypes-operations
    permission-routes/permissions-routes
    navigation-routes/navigation
    stat-routes/stat-gatherer
    path-lists/path-list-creator
    sharing-routes/sharing-routes
    ticket-routes/ticket-routes
    trash-routes/trash
    users-routes/users-routes
    groups-routes/groups-routes
    (undocumented (route/not-found (svc/unrecognized-path-response)))))
