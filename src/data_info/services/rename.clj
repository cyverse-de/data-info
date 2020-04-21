(ns data-info.services.rename
  (:use [clojure-commons.error-codes]
        [clj-jargon.item-ops :only [move move-all]]
        [slingshot.slingshot :only [try+ throw+]])
  (:require [clojure.tools.logging :as log]
            [clojure-commons.file-utils :as ft]
            [dire.core :refer [with-pre-hook! with-post-hook!]]
            [data-info.clients.async-tasks :as async-tasks]
            [data-info.services.uuids :as uuids]
            [data-info.services.directory :as directory]
            [data-info.util.config :as cfg]
            [data-info.util.irods :as irods]
            [data-info.util.logging :as dul]
            [data-info.util.validators :as validators]))

(defn- source->dest
  [source-path dest-path]
  (ft/path-join dest-path (ft/basename source-path)))

(defn- async-thread
  [async-task-id jargon-fn]
  (let [{:keys [username] :as async-task} (async-tasks/get-by-id async-task-id)
        update-fn (fn [path action]
                    (log/info "Updating async task:" async-task-id ":" path action)
                    (async-tasks/add-status async-task-id {:status "running" :detail (format "%s: %s" path (name action))}))]
    (try+
      (async-tasks/add-status async-task-id {:status "started"})
      (irods/with-jargon-exceptions :client-user username [cm]
        (jargon-fn cm async-task update-fn))
      (async-tasks/add-completed-status async-task-id {:status "completed"})
      (catch Object _
        (log/error (:throwable &throw-context) "unable to move paths for async task" async-task-id)
        (async-tasks/add-completed-status async-task-id {:status "failed"})))))


(defn- move-paths-thread
  [async-task-id]
  (let [jargon-fn (fn [cm async-task update-fn]
                    (let [{:keys [username data]} async-task]
                    (move-all cm (:sources data) (:destination data) :user username :admin-users (cfg/irods-admins) :update-fn update-fn)))]
    (async-thread async-task-id jargon-fn)))

(defn- rename-path-thread
  [async-task-id]
  (let [jargon-fn (fn [cm async-task update-fn]
                    (let [{:keys [username data]} async-task]
                    (move cm (:source data) (:destination data) :user username :admin-users (cfg/irods-admins) :update-fn update-fn)))]
    (async-thread async-task-id jargon-fn)))

(defn- move-paths
  "As 'user', moves objects in 'sources' into the directory in 'dest', establishing an asynchronous task and processing in another thread."
  [user sources dest]
  (let [all-paths  (apply merge (mapv #(hash-map (source->dest %1 dest) %1) sources))
        dest-paths (keys all-paths)
        sources    (mapv ft/rm-last-slash sources)
        dest       (ft/rm-last-slash dest)]
    (irods/with-jargon-exceptions :client-user user [cm]
      (validators/user-exists cm user)
      (validators/all-paths-exist cm sources)
      (validators/all-paths-exist cm [dest])
      (validators/path-is-dir cm dest)
      (validators/user-owns-paths cm user sources)
      (validators/path-writeable cm user dest)
      (validators/no-paths-exist cm dest-paths))
    (let [async-task-id (async-tasks/create-task {:type "data-move" :username user :data {:sources sources :destination dest} :statuses [{:status "registered"}]})
          ^Runnable task-thread #(move-paths-thread async-task-id)]
      (log/warn async-task-id)
      (.start (Thread. task-thread (str "data-move-" async-task-id)))
      {:user user :sources sources :dest dest :async-task-id async-task-id})))

(defn- rename-path
  "As 'user', move 'source' to 'dest', establishing an asynchronous task and processing in another thread."
  [user source dest]
  (let [source    (ft/rm-last-slash source)
        dest      (ft/rm-last-slash dest)
        src-base  (ft/basename source)
        dest-base (ft/basename dest)]
    (if (= source dest)
      {:source source :dest dest :user user}
      (do
        (irods/with-jargon-exceptions :client-user user [cm]
          (validators/user-exists cm user)
          (validators/all-paths-exist cm [source (ft/dirname dest)])
          (validators/path-is-dir cm (ft/dirname dest))
          (validators/user-owns-path cm user source)
          (if-not (= (ft/dirname source) (ft/dirname dest))
            (validators/path-writeable cm user (ft/dirname dest)))
          (validators/path-not-exists cm dest))
        (let [async-task-id (async-tasks/create-task {:type "data-rename" :username user :data {:source source :destination dest} :statuses [{:status "registered"}]})
              ^Runnable task-thread #(rename-path-thread async-task-id)]
          (log/warn async-task-id)
          (.start (Thread. task-thread (str "data-rename-" async-task-id)))
          {:user user :source source :dest dest :async-task-id async-task-id})))))

(defn- rename-uuid
  "Rename by UUID: given a user, a source file UUID, and a new name, rename within the same folder."
  [user source-uuid dest-base]
  (let [source (ft/rm-last-slash (uuids/path-for-uuid user source-uuid))
        src-dir (ft/dirname source)
        dest (str (ft/add-trailing-slash src-dir) dest-base)]
    (validators/validate-num-paths-under-folder user source)
    (rename-path user source dest)))

(defn do-rename-uuid
  [{user :user} {dest-base :filename} source-uuid]
  (rename-uuid user source-uuid dest-base))

(defn- move-uuid
  "Rename by UUID: given a user, a source file UUID, and a new directory, move retaining the filename."
  [user source-uuid dest-dir]
  (let [source (ft/rm-last-slash (uuids/path-for-uuid user source-uuid))
        src-base (ft/basename source)
        dest (str (ft/add-trailing-slash dest-dir) src-base)]
    (validators/validate-num-paths-under-folder user source)
    (rename-path user source dest)))

(defn do-move-uuid
  [{user :user} {dest-dir :dirname} source-uuid]
  (move-uuid user source-uuid dest-dir))

(defn do-move
  [{user :user} {sources :sources dest :dest}]
  (move-paths user sources dest))

(defn- move-uuid-contents
  "Rename by UUID: given a user, a source directory UUID, and a new directory, move the directory contents, retaining the filename."
  [user source-uuid dest-dir]
  (let [source (ft/rm-last-slash (uuids/path-for-uuid user source-uuid))]
    (irods/with-jargon-exceptions [cm]
      (validators/path-is-dir cm source))
    (let [sources (directory/get-paths-in-folder user source)]
      (move-paths user sources dest-dir))))

(defn do-move-uuid-contents
  [{user :user} {dest-dir :dirname} source-uuid]
  (move-uuid-contents user source-uuid dest-dir))

(with-post-hook! #'do-rename-uuid (dul/log-func "do-rename-uuid"))

(with-pre-hook! #'do-rename-uuid
  (fn [params body source-uuid]
    (dul/log-call "do-rename-uuid" params body source-uuid)))

(with-post-hook! #'do-move-uuid (dul/log-func "do-move-uuid"))

(with-pre-hook! #'do-move-uuid
  (fn [params body source-uuid]
    (dul/log-call "do-move-uuid" params body source-uuid)))

(with-post-hook! #'do-move-uuid-contents (dul/log-func "do-move-uuid-contents"))

(with-pre-hook! #'do-move-uuid-contents
  (fn [params body source-uuid]
    (dul/log-call "do-move-uuid-contents" params body source-uuid)))

(with-pre-hook! #'do-move
  (fn [params body]
    (dul/log-call "do-move" params body)
    (validators/validate-num-paths-under-paths (:user params) (:sources body))))

(with-post-hook! #'do-move (dul/log-func "do-move"))
