(ns data-info.services.home
  (:require [dire.core :refer [with-pre-hook! with-post-hook!]]
            [clj-irods.validate :refer [validate]]
            [clj-jargon.item-info :refer [exists?]]
            [clj-jargon.item-ops :refer [mkdirs]]
            [data-info.services.stat :as stat]
            [data-info.util.config :as cfg]
            [data-info.util.logging :as log]
            [data-info.util.irods :as irods]
            [data-info.util.paths :as path]))

(defn- user-home-path
  [user zone]
  (let [user-home (path/user-home-dir user)]
    (irods/with-irods-exceptions {} irods
      (validate irods [:user-exists user zone])
      (when-not (exists? @(:jargon irods) user-home)
        (mkdirs @(:jargon irods) user-home))
      (stat/path-stat irods user user-home :filter-include [:id :label :path :date-created :date-modified :permission]))))

(defn do-homedir
  [{user :user}]
  (user-home-path user (cfg/irods-zone)))

(with-pre-hook! #'do-homedir
  (fn [params]
    (log/log-call "do-homedir" params)))

(with-post-hook! #'do-homedir (log/log-func "do-homedir"))
