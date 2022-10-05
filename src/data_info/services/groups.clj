(ns data-info.services.groups
  (:require [clojure.tools.logging :as log]
            [clj-jargon.users :as users]
            [data-info.services.users :as di-users]
            [data-info.util.config :as cfg]
            [data-info.util.irods :as irods]))

(defn get-group
  [{user :user} group-name]
  (irods/with-jargon-exceptions [cm]
    ;; validate
    {:group-name group-name :members (users/list-group-members cm group-name)}))

(defn create-group
  [{user :user} {group-name :group-name members :members}]
  (irods/with-jargon-exceptions [cm]
    ;; need to validate
    (users/create-user-group cm group-name)
    (log/info "group" group-name "should be created")
    (when (seq members)
      (dorun (map
               (partial users/add-to-group cm group-name)
               members)))
    {:group-name group-name :members (users/list-group-members cm group-name)}))

(defn update-group-members
  [{user :user :as params} {group-name :group-name members :members}]
  (irods/with-jargon-exceptions [cm]
    {:group-name group-name :members (users/list-group-members cm group-name)}))

(defn delete-group
  [{user :user} group-name])
