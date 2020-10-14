(ns data-info.util.irods
  "This namespace encapsulates all of the common iRODS access logic."
  (:require [clojure.tools.logging :as log]
            [slingshot.slingshot :refer [try+ throw+]]
            [clj-irods.core :as irods]
            [clj-jargon.by-uuid :as uuid]
            [clj-jargon.init :as init]
            [clj-jargon.item-ops :as ops]
            [clj-jargon.metadata :as meta]
            [otel.otel :as otel]
            [clojure-commons.error-codes :as error]
            [clojure-commons.file-utils :as file]
            [data-info.util.config :as cfg])
  (:import [clojure.lang IPersistentMap]
           [java.util UUID]
           [java.io IOException InputStream]
           [org.irods.jargon.core.exception JargonException]
           [org.apache.tika Tika]))

(defmacro catch-jargon-io-exceptions
  [& body]
  `(try+
     (do ~@body)
     (catch JargonException e#
       (if (instance? IOException (.getCause e#))
         (throw+ {:error_code error/ERR_UNAVAILABLE
                  :reason (str "iRODS is unavailable: " e#)})
         (throw+)))))

(defmacro with-jargon-exceptions
  [& params]
  (let [[opts [[cm-sym] & body]] (split-with #(not (vector? %)) params)]
    `(catch-jargon-io-exceptions
       (init/with-jargon (cfg/jargon-cfg) ~@opts [~cm-sym] (do ~@body)))))


(defmacro with-irods-exceptions
  [more-cfg & params]
  `(catch-jargon-io-exceptions
     (irods/with-irods (assoc ~more-cfg :jargon-cfg (cfg/jargon-cfg)) ~@params)))

(defn ^String abs-path
  "Resolves a path relative to a zone into its absolute path.

   Parameters:
     zone         - the name of the zone
     path-in-zone - the path relative to the zone

   Returns:
     It returns the absolute path."
  [^String zone ^String path-in-zone]
  (file/path-join "/" zone path-in-zone))


(defn ^UUID lookup-uuid
  "Retrieves the UUID associated with a given entity path.

   Parameters:
     cm   - the jargon context map
     path - the path to the entity

   Returns:
     It returns the UUID."
  [^IPersistentMap cm ^String path & {:keys [known-type] :or {known-type nil}}]
  (let [attrs (meta/get-attribute cm path uuid/uuid-attr :known-type known-type)]
    (when-not (pos? (count attrs))
      (log/warn "Missing UUID for" path)
      (throw+ {:error_code error/ERR_NOT_FOUND :path path}))
    (-> attrs first :value UUID/fromString)))

(defn- detect-media-type-from-contents
  [cm ^String path & [istream-ref]]
  (let [^InputStream istream (if istream-ref @istream-ref (ops/input-stream cm path))]
    (try+
      (otel/with-span [s ["Tika detect (InputStream)"]]
        (.detect (Tika.) istream))
      (finally (when-not istream-ref (otel/with-span [s ["close istream"]] (.close istream)))))))

(defn ^String detect-media-type
  "detects the media type of a given file

   Parameters:
     cm   - (OPTIONAL) an open jargon context
     path - the absolute path to the file

   Returns:
     It returns the media type."
  ([cm ^String path & [istream-ref]]
   (otel/with-span [s ["detect-media-type"]]
     (let [path-type (.detect (Tika.) (file/basename path))]
       (if (or (= path-type "application/octet-stream")
               (= path-type "text/plain"))
         (detect-media-type-from-contents (if (delay? cm) @cm cm) path istream-ref)
         path-type))))

  ([^String path]
   (with-jargon-exceptions :lazy true [cm]
     (detect-media-type cm path))))
