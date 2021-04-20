(ns data-info.services.write
  (:require [clojure-commons.file-utils :as ft]
            [clojure-commons.error-codes :as ce]
            [clojure.tools.logging :as log]
            [clojure.java.io :as io]
            [clj-jargon.item-info :as info]
            [clj-jargon.item-ops :as ops]
            [clj-jargon.metadata :as meta]
            [clj-irods.core :as rods]
            [clj-irods.validate :refer [validate]]
            [heuristomancer.core :as hm]
            [ring.middleware.multipart-params :as multipart]
            [otel.otel :as otel]
            [data-info.services.stat :as stat]
            [data-info.services.stat.common :refer [process-filters]]
            [data-info.services.uuids :as uuids]
            [data-info.util.config :as cfg]
            [data-info.util.irods :as irods]
            [data-info.util.validators :as validators])
  (:import [java.io InputStream]))

(defn- reset-on-close-istream
  [^InputStream istream]
  (proxy [InputStream] []
    (available [] (.available istream))
    (mark [readlimit] (.mark istream readlimit))
    (markSupported [] (.markSupported istream))
    (read
      ([] (.read istream))
      ([b] (.read istream b))
      ([b off len] (.read istream b off len)))
    (reset [] (.reset istream))
    (skip [n] (.skip istream n))
    (close []
      (.reset istream))))

(defn- get-info-type
  [istream-ref]
  (otel/with-span [s ["get-info-type"]]
    (.mark @istream-ref (cfg/type-detect-read-amount))
    (let [data (hm/sip (reset-on-close-istream @istream-ref) (cfg/type-detect-read-amount))]
      (future
        (otel/with-span [s ["identify-sample"]]
          (let [result (hm/identify-sample data)]
            (if-not (nil? result)
              (name result)
              "unknown")))))))

(defn- set-info-type
  [cm path info-type]
  (otel/with-span [s ["set-info-type"]]
    (let [existing (meta/get-attribute cm path (cfg/type-detect-type-attribute) :known-type :file)]
      (if (seq existing)
        (:value (first existing) "")
        (do (log/info "adding type" info-type " to file " path)
            (meta/add-metadata cm path (cfg/type-detect-type-attribute) info-type "ipc-data-info-detected" :known-type :file)
            info-type)))))

(defn- save-file-contents
  "Save an istream to a destination. Relies on upstream functions to validate."
  [irods istream-raw user dest-path set-owner?]
  (otel/with-span [s ["save-file-contents"]]
    (let [istream-ref (delay (io/input-stream istream-raw))
          media-type (irods/detect-media-type (:jargon irods) dest-path istream-ref)
          info-type (get-info-type istream-ref)
          base-stat (ops/copy-stream @(:jargon irods) @istream-ref user dest-path :set-owner? set-owner?)
          final-info-type (set-info-type @(:jargon irods) dest-path @info-type)]
      (log/info "Detected info-type:" @info-type ", final type:" final-info-type)
      ;; we don't want to convert the below to clj-irods without cache
      ;; invalidation, since prior validations would have inaccurate info for this
      ;; new content
      (assoc
       (stat/decorate-stat irods user base-stat (process-filters nil [:content-type :infoType]) :validate? false)
       :infoType     final-info-type
       :content-type media-type))))

(defn- create-at-path
  "Create a new file at dest-path from istream.

   Error if the path exists or if the destination directory does not exist or is not writeable."
  [irods istream user dest-path]
  (otel/with-span [s ["create-at-path"]]
    (let [dest-dir (ft/dirname dest-path)]
      (validate irods
                [:path-not-exists dest-path user (cfg/irods-zone)]
                [:path-exists dest-dir user (cfg/irods-zone)]
                [:path-writeable dest-dir user (cfg/irods-zone)])
      (save-file-contents irods istream user dest-path true))))

(defn- overwrite-path
  "Save new contents for the file at dest-path from istream.

   Error if there is no file at that path or the user lacks write permissions thereupon."
  [irods istream user dest-path]
  (otel/with-span [s ["overwrite-path"]]
    (validate irods
              [:path-exists dest-path user (cfg/irods-zone)]
              [:path-is-file dest-path user (cfg/irods-zone)]
              [:path-readable dest-path user (cfg/irods-zone)])
    (save-file-contents irods istream user dest-path false)))

(defn- multipart-create-handler
  "When partially applied, creates a storage handler for
   ring.middleware.multipart-params/multipart-params-request which stores the file in iRODS."
  [user dest-dir {istream :stream filename :filename}]
  (otel/with-span [s ["multipart-create-handler"]]
    (validators/good-pathname filename)
    (irods/with-irods-exceptions {:jargon-opts {:client-user user}} irods
      (validate irods [:user-exists user (cfg/irods-zone)])
      (let [dest-path (ft/path-join dest-dir filename)]
        (create-at-path irods istream user dest-path)))))

(defn wrap-multipart-create
  "Middleware which saves a new file from a multipart request."
  [handler]
  (fn [{{:keys [user dest]} :params :as request}]
    (handler (multipart/multipart-params-request request {:store (partial multipart-create-handler user dest)}))))

(defn- multipart-overwrite-handler
  "When partially applied, creates a storage handler for
   ring.middleware.multipart-params/multipart-params-request which overwrites the file in iRODS."
  [user path-or-uuid uuid? {istream :stream}]
  (otel/with-span [s ["multipart-overwrite-handler"]]
    (irods/with-irods-exceptions {:jargon-opts {:client-user user}} irods
      (validate irods [:user-exists user (cfg/irods-zone)])
      (let [path (ft/rm-last-slash (if uuid?
                                     @(rods/uuid->path irods path-or-uuid)
                                     path-or-uuid))]
        (overwrite-path irods istream user path)))))

(defn wrap-multipart-overwrite
  "Middleware which overwrites a file's contents from a multipart request."
  [handler]
  (fn [{{:keys [user data-id]} :params :as request}]
    (handler (multipart/multipart-params-request request {:store (partial multipart-overwrite-handler user data-id true)}))))

(defn do-upload
  "Returns a path stat after a file has been uploaded. Intended to only be used with wrap-multipart-* middlewares."
  [_ file]
  {:file file})
