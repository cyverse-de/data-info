(ns data-info.services.write
  (:require [clojure-commons.file-utils :as ft]
            [clojure.tools.logging :as log]
            [clojure.java.io :as io]
            [clj-jargon.item-info :as info]
            [clj-jargon.item-ops :as ops]
            [clj-jargon.metadata :as meta]
            [clj-jargon.validations :as cv]
            [clj-irods.core :as rods]
            [clj-irods.validate :refer [validate]]
            [heuristomancer.core :as hm]
            [ring.middleware.multipart-params :as multipart]
            [data-info.clients.async-tasks :as async-tasks]
            [data-info.services.stat :as stat]
            [data-info.services.stat.common :refer [process-filters]]
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
  (.mark @istream-ref (cfg/type-detect-read-amount))
  (let [data (hm/sip (reset-on-close-istream @istream-ref) (cfg/type-detect-read-amount))]
    (future
      (let [result (hm/identify-sample data)]
        (if-not (nil? result)
          (name result)
          "unknown")))))

(defn- set-info-type
  [cm path info-type]
  (let [existing (meta/get-attribute cm path (cfg/type-detect-type-attribute) :known-type :file)]
    (if (seq existing)
      (:value (first existing) "")
      (do (log/info "adding type" info-type " to file " path)
          (meta/add-metadata cm path (cfg/type-detect-type-attribute) info-type "ipc-data-info-detected" :known-type :file)
          info-type))))

(defn- temp-partial-path
  "Returns a hidden temp path in the same collection as dest-path. Keeping the temp object
   in the same collection makes the final rename a catalog-only (atomic) operation and
   avoids any cross-collection permission fix-ups (see clj-jargon move/fix-perms). If either
   the temporary file name or the file path exceeds the name or path length limit then a
   fallback temporary file path is returned instead."
  [dest-path]
  (let [dirname        (ft/dirname dest-path)
        basename       (ft/basename dest-path)
        suffix         (str ".partial-" (java.util.UUID/randomUUID))
        candidate-name (str "." basename suffix)
        candidate-path (ft/path-join dirname candidate-name)
        fallback-path  (ft/path-join dirname suffix)]
    (if (or (> (count candidate-name) cv/max-filename-length)
            (> (count candidate-path) cv/max-path-length))
      fallback-path
      candidate-path)))

(defn- delete-temp-object
  "Best-effort delete of an orphaned temp upload object on a FRESH connection (the client
   connection is typically broken by the same I/O failure that aborted the upload). Tries a
   hard delete, falling back to a trash (catalog-only) delete. Returns true when the object
   is gone. Throws if the object still exists and cannot be removed (e.g. the replica is
   still locked)."
  [write-path]
  (irods/with-jargon-exceptions [cm]
    (when (info/exists? cm write-path)
      (try
        (ops/delete cm write-path true)
        (catch Throwable _
          (ops/delete cm write-path false))))
    (when (info/exists? cm write-path)
      (throw (ex-info "temp upload object still present after delete" {:path write-path})))
    true))

(defn- cleanup-temp-object
  "Thread body for a temp-upload-cleanup async task. iRODS locks the just-aborted replica for a
   short time, so an immediate delete fails; this retries delete-temp-object with backoff until
   the lock clears (or attempts are exhausted), recording the outcome on the async task."
  [async-task-id]
  (try
    (let [write-path (:path (:data (async-tasks/get-by-id async-task-id)))
          detail     (str "[" (cfg/service-identifier) "] " write-path)
          mark       (fn [add-fn status]
                       (try
                         (add-fn async-task-id {:status status :detail detail})
                         (catch Throwable e
                           (log/error e "failed to record" status "status for partial upload cleanup"))))]
      (mark async-tasks/add-status "running")
      (loop [attempts 6, wait-ms 3000]
        (Thread/sleep wait-ms)
        (let [done? (try (delete-temp-object write-path)
                         (catch Throwable e
                           (log/debug e "deferred cleanup pending for partial upload" write-path)
                           false))]
          (cond
            done?           (do (log/info "cleaned up partial upload object" write-path)
                                (mark async-tasks/add-completed-status "completed"))
            (pos? attempts) (recur (dec attempts) (min 30000 (* 2 wait-ms)))
            :else           (do (log/warn "gave up cleaning up partial upload object" write-path)
                                (mark async-tasks/add-completed-status "failed"))))))
    (catch Throwable e
      (log/error e "partial upload cleanup task failed" async-task-id))))

(defn- schedule-temp-cleanup
  "Registers a tracked async task to clean up an orphaned temp upload object and processes it on
   a background thread (see cleanup-temp-object). Best-effort: a failure to register the task is
   logged and swallowed so it never masks the upload error that triggered the cleanup."
  [write-path user]
  (try
    (async-tasks/run-async-thread
     (async-tasks/create-task
      {:type      "data-upload-cleanup"
       :username  user
       :data      {:path write-path :instance-id (cfg/service-identifier)}
       :statuses  [{:status "registered" :detail (str "[" (cfg/service-identifier) "]")}]
       :behaviors [{:type "statuschangetimeout"
                    :data {:statuses [{:start_status "running" :end_status "detected-stalled" :timeout "10m"}]}}]})
     cleanup-temp-object
     "upload-temp-cleanup")
    (catch Throwable e
      (log/error e "failed to schedule cleanup of partial upload object" write-path))))

(defn- write-stream
  "Streams istream to dest-path and returns the stat of dest-path.

   When atomic? is true, the bytes are written to a temp object in the same collection and
   renamed into place only on success. This keeps a new-file create atomic: an interrupted
   upload never leaves a partial object at dest-path (which would otherwise cause a spurious
   ERR_EXISTS on the next attempt). The orphaned temp object is cleaned up asynchronously."
  [cm istream user dest-path set-owner? atomic?]
  (if-not atomic?
    (ops/copy-stream cm istream user dest-path :set-owner? set-owner?)
    (let [write-path (temp-partial-path dest-path)]
      (try
        (ops/copy-stream cm istream user write-path :set-owner? set-owner?)
        (ops/move cm write-path dest-path :user user)
        (info/stat cm dest-path)
        (catch Throwable t
          (schedule-temp-cleanup write-path user)
          (throw t))))))

(defn- save-file-contents
  "Save an istream to a destination. Relies on upstream functions to validate.

   When atomic? is true (new-file creates), the contents are streamed to a temp object and
   renamed into place on success so an interrupted upload never orphans a partial file at
   dest-path."
  [irods istream-raw user dest-path set-owner? atomic?]
  (let [cm          @(:jargon irods)
        istream-ref (delay (io/input-stream istream-raw))
        media-type (irods/detect-media-type (:jargon irods) dest-path istream-ref)
        info-type (get-info-type istream-ref)
        base-stat (write-stream cm @istream-ref user dest-path set-owner? atomic?)
        final-info-type (irods/with-jargon-exceptions [admin-cm] (set-info-type admin-cm dest-path @info-type))]
    (log/info "Detected info-type:" @info-type ", final type:" final-info-type)
    (rods/invalidate irods dest-path)
    (assoc
     (stat/decorate-stat irods user (cfg/irods-zone) base-stat (process-filters nil [:content-type :infoType]) :validate? false)
     :infoType     final-info-type
     :content-type media-type)))

(defn- create-at-path
  "Create a new file at dest-path from istream.

   Error if the path exists or if the destination directory does not exist or is not writeable."
  [irods istream user dest-path]
  (let [dest-dir (ft/dirname dest-path)]
    (validate irods
              [:path-not-exists dest-path user (cfg/irods-zone)]
              [:path-exists dest-dir user (cfg/irods-zone)]
              [:path-writeable dest-dir user (cfg/irods-zone)])
    (save-file-contents irods istream user dest-path true true)))

(defn- overwrite-path
  "Save new contents for the file at dest-path from istream.

   Error if there is no file at that path or the user lacks write permissions thereupon."
  [irods istream user dest-path]
  (validate irods
            [:path-exists dest-path user (cfg/irods-zone)]
            [:path-is-file dest-path user (cfg/irods-zone)]
            [:path-readable dest-path user (cfg/irods-zone)])
  (save-file-contents irods istream user dest-path false false))

(defn- multipart-create-handler
  "When partially applied, creates a storage handler for
   ring.middleware.multipart-params/multipart-params-request which stores the file in iRODS."
  [user dest-dir {istream :stream filename :filename}]
  (validators/good-pathname filename)
  (irods/with-irods-exceptions {:jargon-opts {:client-user user}} irods
    (validate irods [:user-exists user (cfg/irods-zone)])
    (let [dest-path (ft/path-join dest-dir filename)]
      (create-at-path irods istream user dest-path))))

(defn wrap-multipart-create
  "Middleware which saves a new file from a multipart request."
  [handler]
  (fn [{{:keys [user dest]} :params :as request}]
    (handler (multipart/multipart-params-request request {:store (partial multipart-create-handler user dest)}))))

(defn- multipart-overwrite-handler
  "When partially applied, creates a storage handler for
   ring.middleware.multipart-params/multipart-params-request which overwrites the file in iRODS."
  [user path-or-uuid uuid? {istream :stream}]
  (irods/with-irods-exceptions {:jargon-opts {:client-user user}} irods
    (validate irods [:user-exists user (cfg/irods-zone)])
    (let [path (ft/rm-last-slash (if uuid?
                                   @(rods/uuid->path irods path-or-uuid)
                                   path-or-uuid))]
      (overwrite-path irods istream user path))))

(defn wrap-multipart-overwrite
  "Middleware which overwrites a file's contents from a multipart request."
  [handler]
  (fn [{{:keys [user data-id]} :params :as request}]
    (handler (multipart/multipart-params-request request {:store (partial multipart-overwrite-handler user data-id true)}))))

(defn do-upload
  "Returns a path stat after a file has been uploaded. Intended to only be used with wrap-multipart-* middlewares."
  [_ file]
  {:file file})
