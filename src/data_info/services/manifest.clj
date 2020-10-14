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

(def ^:private coge-attr "ipc-coge-link")

;; this is usually going to be the vector case, but the previous behavior is the map case so it's preserved
(defn- postprocess-tree-urls
  [tree-urls]
  (if (map? tree-urls)
      (:tree-urls tree-urls)
      (vec tree-urls)))

(defn- extract-tree-urls
  [irods user fpath]
  (log/info (:cache irods))
  (let [avu (rods/object-avu irods user (cfg/irods-zone) fpath {:attr (cfg/tree-urls-attr)})]
    (if (and avu (pos? (count @avu)))
      (-> @avu
        first
        :value
        ft/basename
        tree/get-tree-urls
        postprocess-tree-urls)
      [])))

(defn- extract-coge-view
  [irods user fpath]
  (let [avu (rods/object-avu irods user (cfg/irods-zone) fpath {:attr coge-attr})]
    (if (and avu (pos? (count @avu)))
      (mapv (fn [{url :value} idx] {:label (str "gene_" idx) :url url})
            @avu (range))
      [])))

(defn- format-anon-files-url
  [fpath]
  {:label "anonymous" :url (anon-file-url fpath)})

(defn- extract-urls
  [{cm :jargon :as irods} user fpath]
  (otel/with-span [s ["extract-urls"]]
    (let [urls (concat (extract-tree-urls irods user fpath) (extract-coge-view irods user fpath))]
      (vec (if (anon-readable? @cm fpath)
             (conj urls (format-anon-files-url fpath))
             urls)))))

(defn- manifest-map
  [irods user {:keys [path] :as file}]
  (-> (select-keys file [:content-type :infoType])
      (assoc :urls (extract-urls irods user path))))

(defn- manifest
  [{cm :jargon :as irods} user path]
  (let [path (ft/rm-last-slash path)]
    (validate irods
              [:path-exists path user (cfg/irods-zone)]
              [:path-is-file path user (cfg/irods-zone)]
              [:path-readable path user (cfg/irods-zone)])
    (let [file (stat/path-stat @cm user path :filter-include [:path :content-type :infoType])]
      (manifest-map irods user file))))

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
      (validate irods
                [:user-exists user (cfg/irods-zone)])
        (manifest irods user path))))

(with-pre-hook! #'do-manifest-uuid
  (fn [user data-id]
    (dul/log-call "do-manifest-uuid" user data-id)))

(with-post-hook! #'do-manifest-uuid (dul/log-func "do-manifest-uuid"))

(with-pre-hook! #'do-manifest
  (fn [user data-id]
    (dul/log-call "do-manifest" user data-id)))

(with-post-hook! #'do-manifest (dul/log-func "do-manifest"))
