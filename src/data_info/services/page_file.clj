(ns data-info.services.page-file
  (:use [clj-jargon.item-info]
        [clj-jargon.paging]
        [slingshot.slingshot :only [try+ throw+]])
  (:require [clojure.tools.logging :as log]
            [clojure-commons.file-utils :as ft]
            [clj-irods.core :as rods]
            [clj-irods.validate :refer [validate]]
            [otel.otel :as otel]
            [dire.core :refer [with-pre-hook! with-post-hook!]]
            [data-info.services.uuids :as uuids]
            [data-info.util.config :as cfg]
            [data-info.util.irods :as irods]
            [data-info.util.logging :as dul]))

(defn- read-file-chunk
  "Reads a chunk of a file starting at 'position' and reading a chunk of length 'chunk-size'."
  [user path-or-uuid position chunk-size uuid?]
  (otel/with-span [s ["read-file-chunk"]]
    (irods/with-irods-exceptions {:use-icat-transaction false} irods
      (future (force (:jargon irods)))
      (validate irods
                [:user-exists user (cfg/irods-zone)])
      (let [path (ft/rm-last-slash
                   (if uuid?
                     (uuids/path-for-uuid @(:jargon irods) user path-or-uuid)
                     path-or-uuid))]
        (validate irods
                  [:path-exists path user (cfg/irods-zone)]
                  [:path-is-file path user (cfg/irods-zone)]
                  [:path-readable path user (cfg/irods-zone)])
        {:path       path
         :user       user
         :start      (str position)
         :chunk-size (str chunk-size)
         :file-size  (str (rods/file-size irods user (cfg/irods-zone) path))
         :chunk      (read-at-position @(:jargon irods) path position chunk-size)}))))

(defn do-read-chunk-uuid
  [{user :user position :position chunk-size :size} data-id]
  (read-file-chunk user data-id position chunk-size true))

(defn do-read-chunk
  [{user :user position :position chunk-size :size} path]
  (read-file-chunk user path position chunk-size false))

(with-pre-hook! #'do-read-chunk-uuid
  (fn [params data-id]
    (dul/log-call "do-read-chunk-uuid" params data-id)))

(with-pre-hook! #'do-read-chunk
  (fn [params path]
    (dul/log-call "do-read-chunk" params path)))
