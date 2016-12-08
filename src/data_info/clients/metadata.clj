(ns data-info.clients.metadata
  (:require [data-info.util.config :as config]
            [metadata-client.core :as metadata-client]))

(defn list-avus
  [user data-type data-id]
  (metadata-client/list-avus (config/metadata-client) user data-type data-id {:as :json}))

(defn update-avus
  [user data-type data-id body]
  (metadata-client/update-avus (config/metadata-client) user data-type data-id body))

(defn set-avus
  [user data-type data-id body]
  (metadata-client/set-avus (config/metadata-client) user data-type data-id body))

(defn copy-metadata-avus
  [user data-type data-id dest-items]
  (metadata-client/copy-metadata-avus (config/metadata-client) user data-type data-id dest-items))
