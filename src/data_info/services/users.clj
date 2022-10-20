(ns data-info.services.users
  (:require [clojure.tools.logging :as log]
            [clj-jargon.users :as users]
            [dire.core :refer [with-pre-hook! with-post-hook!]]
            [data-info.util.config :as cfg]
            [data-info.services.permissions :as perms]
            [data-info.util.irods :as irods]
            [data-info.util.logging :as dul]
            [data-info.util.validators :as validators]))

(defn list-user-groups
  [cm username]
  (validators/user-exists cm username)
  (users/user-groups cm username))

(defn qualify-username
  [name]
  (str name \# (cfg/irods-zone)))

(defn ensure-qualified
  [name]
  (if (re-find #"#" name)
    name
    (qualify-username name)))

(defn do-list-qualified-user-groups
  [user username]
  (irods/with-jargon-exceptions :client-user user [cm]
      {:groups (map qualify-username (list-user-groups cm username))
       :user (qualify-username username)}))

(with-pre-hook! #'do-list-qualified-user-groups
  (fn [user username]
    (dul/log-call "do-list-qualified-user-groups" user username)))

(with-post-hook! #'do-list-qualified-user-groups (dul/log-func "do-list-qualified-user-groups"))

(defn- list-perm
  [cm user abspath]
  {:path abspath
   :user-permissions (perms/filtered-user-perms cm user abspath)})

(defn- list-perms
  [user abspaths]
  (irods/with-jargon-exceptions [cm]
    (validators/user-exists cm user)
    (validators/all-paths-exist cm abspaths)
    (validators/user-owns-paths cm user abspaths)
    (mapv (partial list-perm cm user) abspaths)))

(defn do-user-permissions
  [{user :user} {paths :paths}]
  {:paths (list-perms user paths)})

(with-pre-hook! #'do-user-permissions
  (fn [params body]
    (dul/log-call "do-user-permissions" params body)
    (validators/validate-num-paths (:paths body))))

(with-post-hook! #'do-user-permissions (dul/log-func "do-user-permissions"))
