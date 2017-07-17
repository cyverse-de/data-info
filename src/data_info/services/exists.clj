(ns data-info.services.exists
  (:require [dire.core :refer [with-pre-hook! with-post-hook!]]
            [clj-jargon.item-info :as item]
            [clj-jargon.permissions :as perm]
            [clojure-commons.file-utils :as ft]
            [data-info.util.irods :as irods]
            [data-info.util.logging :as log]
            [data-info.util.validators :as duv]))

(defn- path-exists-for-user?
  [cm user path]
  (item/exists? cm (ft/rm-last-slash path)))

(defn do-exists
  [{user :user} {paths :paths}]
  ;; The client user below ensures that iRODS won't show things the user doesn't have access to.
  ;; Without it, an extra check needs to be added in path-exists-for-user? for that.
  ;; Additionally, this checks that the user exists, which would otherwise need to be validated.
  (irods/with-jargon-exceptions :client-user user [cm]
    {:paths (into {} (map (juxt keyword (partial path-exists-for-user? cm user)) (set paths)))}))

(with-pre-hook! #'do-exists
  (fn [params body]
    (log/log-call "do-exists" params)
    (duv/validate-num-paths (:paths body))))

(with-post-hook! #'do-exists (log/log-func "do-exists"))
