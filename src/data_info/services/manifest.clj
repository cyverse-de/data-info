(ns data-info.services.manifest
  (:use [data-info.services.sharing :only [anon-file-url anon-readable?]]
        [clj-irods.core :as rods]
        [clj-jargon.metadata :only [get-attribute attribute?]]
        [slingshot.slingshot :only [try+ throw+]])
  (:require [clojure.tools.logging :as log]
            [clojure-commons.file-utils :as ft]
            [clj-irods.validate :refer [validate]]
            [otel.otel :as otel]
            [dire.core :refer [with-pre-hook! with-post-hook!]]
            [data-info.util.validators :as validators]
            [data-info.util.irods :as irods]
            [data-info.util.logging :as dul]
            [data-info.util.config :as cfg]))

(defn- format-anon-files-url
  [fpath]
  {:label "anonymous" :url (anon-file-url fpath)})

(defn- extract-urls
  [irods user fpath]
  (otel/with-span [s ["extract-urls"]]
    (let [readable (anon-readable? irods fpath)]
      (future (if @readable [(format-anon-files-url fpath)] [])))))

(defn- manifest
  [user path-or-uuid uuid?]
  (otel/with-span [s ["manifest"]]
    (irods/with-irods-exceptions {:use-icat-transaction false} irods
      (validate irods [:user-exists user (cfg/irods-zone)])
      (let [path (ft/rm-last-slash
                   (if uuid?
                     @(rods/uuid->path irods path-or-uuid)
                     path-or-uuid))]
        (validate irods
                  [:path-exists path user (cfg/irods-zone)]
                  [:path-is-file path user (cfg/irods-zone)]
                  [:path-readable path user (cfg/irods-zone)])
        (let [urls (extract-urls irods user path)
              info-type (rods/info-type irods user (cfg/irods-zone) path)]
          {:content-type (irods/detect-media-type (:jargon irods) path)
           :infoType     (or @info-type "unknown")
           :urls @urls})))))

(defn do-manifest-uuid
  [user data-id]
  (manifest user data-id true))

(defn do-manifest
  [user path]
  (manifest user path false))

(with-pre-hook! #'do-manifest-uuid
  (fn [user data-id]
    (dul/log-call "do-manifest-uuid" user data-id)))

(with-post-hook! #'do-manifest-uuid (dul/log-func "do-manifest-uuid"))

(with-pre-hook! #'do-manifest
  (fn [user data-id]
    (dul/log-call "do-manifest" user data-id)))

(with-post-hook! #'do-manifest (dul/log-func "do-manifest"))
