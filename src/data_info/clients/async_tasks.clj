(ns data-info.clients.async-tasks
  (:use [slingshot.slingshot :only [try+]])
  (:require [data-info.util.config :as config]
            [data-info.util.irods :as irods]
            [io.aviso.exception :as exception]
            [clojure.tools.logging :as log]
            [clojure.string :as string]
            [async-tasks-client.core :as async-tasks-client]))


(defn get-by-id
  [id]
  (async-tasks-client/get-by-id (config/async-tasks-client) id))

(defn delete-by-id
  [id]
  (async-tasks-client/delete-by-id (config/async-tasks-client) id))

(defn create-task
  [task]
  (async-tasks-client/create-task (config/async-tasks-client) task))

(defn add-status
  [id status]
  (async-tasks-client/add-status (config/async-tasks-client) id status))

(defn add-completed-status
  [id status]
  (async-tasks-client/add-completed-status (config/async-tasks-client) id status))

(defn add-behavior
  [id behavior]
  (async-tasks-client/add-behavior (config/async-tasks-client) id behavior))

(defn get-by-filter
  [filters]
  (async-tasks-client/get-by-filter (config/async-tasks-client) filters))

(defn run-async-thread
  [async-task-id thread-function prefix]
  (let [^Runnable task-thread (fn [] (thread-function async-task-id))]
    (.start (Thread. task-thread (str prefix "-" (string/replace async-task-id #".*/tasks/" "")))))
  async-task-id)

(defn paths-async-thread
  ([async-task-id jargon-fn end-fn]
   (paths-async-thread async-task-id jargon-fn end-fn true))
  ([async-task-id jargon-fn end-fn use-client-user?]
   (let [{:keys [username] :as async-task} (get-by-id async-task-id)
         update-fn (fn [path action]
                     (log/info "Updating async task:" async-task-id ":" path action)
                     (add-status async-task-id {:status "running" :detail (format "%s: %s" path (name action))}))]
     (try+
       (add-status async-task-id {:status "started"})
       (if use-client-user?
         (irods/with-jargon-exceptions :client-user username [cm]
           (jargon-fn cm async-task update-fn))
         (irods/with-jargon-exceptions [cm]
           (jargon-fn cm async-task update-fn)))
       (add-completed-status async-task-id {:status "completed"})
       (end-fn async-task false)
       (catch Object _
         (log/error (:throwable &throw-context) "failed processing async task" async-task-id)
         (add-completed-status async-task-id {:status "failed" :detail (exception/format-exception (:throwable &throw-context))})
         (end-fn async-task true))))))
