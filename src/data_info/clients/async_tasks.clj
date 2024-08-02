(ns data-info.clients.async-tasks
  (:use [slingshot.slingshot :only [try+ throw+]])
  (:require [data-info.util.config :as config]
            [data-info.util.irods :as irods]
            [clojure.tools.logging :as log]
            [clojure.string :as string]
            [clj-irods.core :refer [make-threadpool]]
            [clj-irods.cache-tools :as c]
            [async-tasks-client.core :as async-tasks-client]))

;; https://stackoverflow.com/questions/12068640/retrying-something-3-times-before-throwing-an-exception-in-clojure
;; updated to use slingshot try+/throw+ and allow a configurable handler function
;; you can pass something like (fn [e t] (throw+ (:throwable t))) to rethrow if the retries fail
(defn- retry-with-handler
  [retries handler f & args]
  (let [res (try+ {::value (apply f args)}
                 (catch Object e
                   (if (zero? retries)
                     (handler e &throw-context)
                     {::exception e})))]
    (if (::exception res)
      (do
        (Thread/sleep 2000) ;; arbitrary wait to allow for things to improve
        (recur (dec retries) handler f args))
      (::value res))))

(defn- retry-via-threadpool
  [pool retries handler f & args]
  (let [ag (agent nil)]
    (send-via pool ag (fn [_nil] (try+
                                  (apply retry-with-handler retries handler f args)
                                  (catch Object o
                                    {::error o}))))
    (delay (if-let [err (::error @ag)]
             (throw+ err)
             @ag))))

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
    (.start (Thread. task-thread (str prefix "-" (string/replace async-task-id #".*/tasks/" ""))))))

(defn create-update-fn [async-task-id pool]
  (fn [path action]
    (log/info "Updating async task:" async-task-id ":" path action)
    ;; we use the thread pool version here to be non-blocking, but elsewhere we use it so things happen in order (but
    ;; deref the result to wait for the result before continuing)
    (retry-via-threadpool
     pool 3
     (fn [e t] (log/error (:throwable t) "failed updating async task"))
     add-status async-task-id {:status "running"
                               :detail (format "[%s] %s: %s" (config/service-identifier) path (name action))})))

(defn paths-async-thread
  ([async-task-id jargon-fn end-fn]
   (paths-async-thread async-task-id jargon-fn end-fn true))
  ([async-task-id jargon-fn end-fn use-client-user?]
   (let [pool                              (make-threadpool (str async-task-id "-async-tasks-update") 1)
         {:keys [username] :as async-task} (get-by-id async-task-id)
         update-fn                         (create-update-fn async-task-id pool)]
     (try+
      (deref (retry-via-threadpool pool 3
                                   (fn [e t] (log/error (:throwable t) "failed updating async task with started status"))
                                   add-status async-task-id {:status "started"}))
      (if use-client-user?
        (irods/with-jargon-exceptions :client-user username [cm]
          (jargon-fn cm async-task update-fn))
        (irods/with-jargon-exceptions [cm] (jargon-fn cm async-task update-fn)))
      ;; For the completed statuses we want a lot of retries because the presence or absence of an end date controls
      ;; locking behavior
      (deref (retry-via-threadpool
              pool 100
              (fn [e t] (log/error (:throwable t) "failed updating async task with completed status"))
              add-completed-status async-task-id {:status "completed" :detail (str "[" (config/service-identifier) "]")}))
      (end-fn async-task false)
      (catch Object _
        (log/error (:throwable &throw-context) "failed processing async task" async-task-id)
        (deref (retry-via-threadpool
                pool 100
                (fn [e t] (log/error (:throwable t) "failed updating async task with completed status"))
                add-completed-status async-task-id
                {:status "failed"
                 :detail (format "[%s] %s" (config/service-identifier) (pr-str (:throwable &throw-context)))}))
        (end-fn async-task true))))))
