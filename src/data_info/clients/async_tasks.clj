(ns data-info.clients.async-tasks
  (:require [data-info.util.config :as config]
            [async-tasks-client.core :as async-tasks-client]))


(defn get-by-id
  [id]
  (async-tasks-client/get-by-id (config/async-tasks-client) id))
