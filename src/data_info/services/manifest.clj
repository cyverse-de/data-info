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
            [data-info.services.stat :as stat]
            [data-info.services.uuids :as uuids]
            [data-info.util.irods :as irods]
            [data-info.util.logging :as dul]
            [tree-urls-client.core :as tree]
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
  [{cm :jargon :as irods} user path]
  (let [path (ft/rm-last-slash path)]
    (validate irods
              [:path-exists path user (cfg/irods-zone)]
              [:path-is-file path user (cfg/irods-zone)]
              [:path-readable path user (cfg/irods-zone)])
    (let [urls (extract-urls irods user path)
          file (stat/path-stat @cm user path :filter-include [:path :content-type :infoType])]
      {:content-type (:content-type file)
       :infoType     (:infoType file)
       :urls @urls})))

(defn do-manifest-uuid
  [user data-id]
  (otel/with-span [s ["do-manifest-uuid"]]
    (irods/with-irods-exceptions {:use-icat-transaction false} irods
      (validate irods [:user-exists user (cfg/irods-zone)])
      (let [path (uuids/path-for-uuid @(:jargon irods) user data-id)]
        (manifest irods user path)))))

(defn do-manifest
  [user path]
  (otel/with-span [s ["do-manifest"]]
    (irods/with-irods-exceptions {:use-icat-transaction false} irods
      (validate irods [:user-exists user (cfg/irods-zone)])
        (manifest irods user path))))

(with-pre-hook! #'do-manifest-uuid
  (fn [user data-id]
    (dul/log-call "do-manifest-uuid" user data-id)))

(with-post-hook! #'do-manifest-uuid (dul/log-func "do-manifest-uuid"))

(with-pre-hook! #'do-manifest
  (fn [user data-id]
    (dul/log-call "do-manifest" user data-id)))

(with-post-hook! #'do-manifest (dul/log-func "do-manifest"))
