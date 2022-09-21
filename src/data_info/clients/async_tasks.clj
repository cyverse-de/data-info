(ns data-info.clients.async-tasks
  (:use [slingshot.slingshot :only [try+ throw+]])
  (:require [data-info.util.config :as config]
            [data-info.util.irods :as irods]
            [clojure.tools.logging :as log]
            [clojure.string :as string]
            [otel.otel :as otel]
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
      (recur (dec retries) handler f args)
      (::value res))))

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
  (otel/with-span [outer-span ["run-async-thread" {:kind :producer :attributes {"async-task-id" (str async-task-id)}}]]
    (let [^Runnable task-thread (fn []
                                  (with-open [_ (otel/span-scope outer-span)]
                                    (otel/with-span [s ["async thread" {:kind :consumer :attributes {"async-task-id" (str async-task-id)}}]]
                                      (thread-function async-task-id))))]
      (.start (Thread. task-thread (str prefix "-" (string/replace async-task-id #".*/tasks/" "")))))
    async-task-id))

(defn paths-async-thread
  ([async-task-id jargon-fn end-fn]
   (paths-async-thread async-task-id jargon-fn end-fn true))
  ([async-task-id jargon-fn end-fn use-client-user?]
   (otel/with-span [s ["paths-async-thread" {:attributes {"async-task-id" (str async-task-id)}}]]
     (let [{:keys [username] :as async-task} (get-by-id async-task-id)
           update-fn (fn [path action]
                       (otel/with-span [s ["update-fn"]]
                         (log/info "Updating async task:" async-task-id ":" path action)
                         (retry-with-handler 3
                           (fn [e t] (log/error (:throwable t) "failed updating async task"))
                           add-status async-task-id {:status "running" :detail (format "[%s] %s: %s" (config/service-identifier) path (name action))})))]
       (try+
         (retry-with-handler 3
           (fn [e t] (log/error (:throwable t) "failed updating async task with started status"))
           add-status async-task-id {:status "started"})
         (if use-client-user?
           (irods/with-jargon-exceptions :client-user username [cm]
             (otel/with-span [s ["jargon-fn" {:attributes {"client-user" username}}]]
               (jargon-fn cm async-task update-fn)))
           (irods/with-jargon-exceptions [cm]
             (otel/with-span [s ["jargon-fn"]]
               (jargon-fn cm async-task update-fn))))
           ;; For the completed statuses we want a lot of retries because the presence or absence of an end date controls locking behavior
           (retry-with-handler 100
             (fn [e t] (log/error (:throwable t) "failed updating async task with completed status"))
             add-completed-status async-task-id {:status "completed" :detail (str "[" (config/service-identifier) "]")})
         (otel/with-span [s ["end-fn"]]
           (end-fn async-task false))
         (catch Object _
           (log/error (:throwable &throw-context) "failed processing async task" async-task-id)
           (retry-with-handler 100
             (fn [e t] (log/error (:throwable t) "failed updating async task with completed status"))
             add-completed-status async-task-id {:status "failed" :detail (format "[%s] %s" (config/service-identifier) (pr-str (:throwable &throw-context)))})
           (end-fn async-task true)))))))
