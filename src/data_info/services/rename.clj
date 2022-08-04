(ns data-info.services.rename
  (:use [clojure-commons.error-codes]
        [clj-jargon.item-ops :only [move move-all]]
        [slingshot.slingshot :only [try+ throw+]])
  (:require [clojure.tools.logging :as log]
            [clojure.string :as string]
            [clojure-commons.file-utils :as ft]
            [clojure-commons.error-codes :as error]
            [dire.core :refer [with-pre-hook! with-post-hook!]]
            [data-info.clients.async-tasks :as async-tasks]
            [data-info.clients.notifications :as notifications]
            [data-info.services.uuids :as uuids]
            [data-info.services.directory :as directory]
            [data-info.util.config :as cfg]
            [data-info.util.irods :as irods]
            [data-info.util.logging :as dul]
            [data-info.util.validators :as validators]
            [otel.otel :as otel]))

(defn- source->dest
  [source-path dest-path]
  (ft/path-join dest-path (ft/basename source-path)))

(defn validate-unlocked
  ([sources dests]
   (validate-unlocked (concat sources dests)))
  ([paths]
   ;; We need to:
   ;; Fetch all async tasks that could cause it to be locked (data-move, data-rename, data-delete, data-delete-trash, data-restore)
   ;; only include non-completed ones, i.e. EndDateSince sometime in the future + include null end
   ;; Pull out their paths.
   ;; data-move: sources, destination
   ;; data-rename: source, destination
   ;; data-delete: paths, trash-paths (not that the latter is real likely)
   ;; data-delete-trash: trash-paths
   ;; data-restore: paths, restoration-paths
   ;; Then we need to check the paths. No source or destination can be the source or destination of another move.
   ;; This includes exact matches but also prefix matches in both directions:
   ;; If a locked source plus '/' is a prefix of a new source, we're moving the new source with both processes
   ;; If a locked destination plus '/' is a prefix of a new source, we're trying to move the new source out while it's still being moved in
   ;; If a new source plus '/' is a prefix of a locked source, we're moving the locked source with both processes
   ;; If a new source plus '/' is a prefix of a locked destination, we're  trying to move something in while part of it (the locked dest) is being moved out
   ;; Locked source + '/' prefix of new dest, we're moving new dest in while the parent is being moved out
   ;; Locked dest + '/' prefix of new dest, we're moving stuff into new dest with two processes
   ;; New dest + '/' prefix of locked source, we might be moving stuff into locked source while it's being moved out
   ;; New dest + '/' prefix of locked dest, we're moving stuff into locked dest with two processes
   (let [far-future "9999-12-31T23:59:59Z"
         eligible-async-task-types ["data-move" "data-rename" "data-delete" "data-delete-trash" "data-restore"]
         eligible-tasks (async-tasks/get-by-filter {:type eligible-async-task-types
                                                    :include_null_end true
                                                    :end_date_since far-future})
         add-destination-to-basenames (fn [destination sources]
                                        (map #(ft/path-join destination %) (map ft/basename sources)))
         extract-paths (fn [{:keys [data type]}]
                         (condp = type
                           "data-move"         (concat (add-destination-to-basenames (:destination data) (:sources data))
                                                       (:sources data))
                           "data-rename"       (map #(get data %) [:destination :source])
                           "data-delete"       (concat (:paths data) (vals (:trash-paths data)))
                           "data-delete-trash" (:trash-paths data)
                           "data-restore"      (concat (:paths data)
                                                       (map :restored-path (vals (:restoration-paths data))))
                           nil))
         locked-paths (reduce conj #{} (mapcat extract-paths eligible-tasks))
         path-matches (fn [path] (or
                                   (get locked-paths path)
                                   (some #(string/starts-with? (ft/add-trailing-slash %) path) locked-paths) ;; locked path + '/' prefix of new path
                                   (some #(string/starts-with? (ft/add-trailing-slash path) %) locked-paths) ;; new path + '/' prefix of locked path
                                   ))
         matching-paths (filterv path-matches paths)]
     (if (seq matching-paths)
       (throw+ {:error_code error/ERR_CONFLICT
                :paths      matching-paths})))))

(defn- move-paths-thread
  [async-task-id]
  (let [jargon-fn (fn [cm async-task update-fn]
                    (let [{:keys [username data]} async-task]
                    (move-all cm (:sources data) (:destination data) :user username :admin-users (cfg/irods-admins) :update-fn update-fn)))
        end-fn (fn [async-task failed?]
                 (let [src-path->dest-path (fn [src-path]
                                             (ft/path-join (:destination (:data async-task)) (ft/basename src-path)))]
                   (notifications/send-notification
                     (notifications/move-notification
                       (:username async-task)
                       (:sources (:data async-task))
                       (mapv src-path->dest-path (:sources (:data async-task))) failed?))))]
    (async-tasks/paths-async-thread async-task-id jargon-fn end-fn)))

(defn- rename-path-thread
  [async-task-id]
  (let [jargon-fn (fn [cm async-task update-fn]
                    (let [{:keys [username data]} async-task]
                    (move cm (:source data) (:destination data) :user username :admin-users (cfg/irods-admins) :update-fn update-fn)))
        end-fn (fn [async-task failed?]
                 (notifications/send-notification
                   (notifications/rename-notification (:username async-task) [(:source (:data async-task))] [(:destination (:data async-task))] failed?)))]
    (async-tasks/paths-async-thread async-task-id jargon-fn end-fn)))

(defn new-task
  [type user data]
  (async-tasks/create-task
    {:type type
     :username user
     :data (assoc data :instance-id (cfg/service-identifier))
     :statuses [{:status "registered" :detail (str "[" (cfg/service-identifier) "]")}]
     :behaviors [{:type "statuschangetimeout"
                  :data {:statuses [{:start_status "running" :end_status "detected-stalled" :timeout "10m"}]}}]}))

(defn- move-paths
  "As 'user', moves objects in 'sources' into the directory in 'dest', establishing an asynchronous task and processing in another thread."
  [user sources dest]
  (otel/with-span [s ["move-paths"]]
    (let [all-paths  (apply merge (mapv #(hash-map (source->dest %1 dest) %1) sources))
          dest-paths (keys all-paths)
          sources    (mapv ft/rm-last-slash sources)
          dest       (ft/rm-last-slash dest)]
      (validate-unlocked sources dest-paths)
      (irods/with-jargon-exceptions :client-user user [cm]
        (validators/user-exists cm user)
        (validators/all-paths-exist cm sources)
        (validators/all-paths-exist cm [dest])
        (validators/path-is-dir cm dest)
        (validators/user-owns-paths cm user sources)
        (validators/path-writeable cm user dest)
        (validators/no-paths-exist cm dest-paths))
      (let [async-task-id (async-tasks/run-async-thread
                            (new-task "data-move" user {:sources sources :destination dest})
                            move-paths-thread "data-move")]
        {:user user :sources sources :dest dest :async-task-id async-task-id}))))

(defn- rename-path
  "As 'user', move 'source' to 'dest', establishing an asynchronous task and processing in another thread."
  [user source dest]
  (otel/with-span [s ["rename-path"]]
    (let [source    (ft/rm-last-slash source)
          dest      (ft/rm-last-slash dest)
          src-base  (ft/basename source)
          dest-base (ft/basename dest)]
      (if (= source dest)
        {:source source :dest dest :user user}
        (do
          (validate-unlocked [source dest])
          (irods/with-jargon-exceptions :client-user user [cm]
            (validators/user-exists cm user)
            (validators/all-paths-exist cm [source (ft/dirname dest)])
            (validators/path-is-dir cm (ft/dirname dest))
            (validators/user-owns-path cm user source)
            (if-not (= (ft/dirname source) (ft/dirname dest))
              (validators/path-writeable cm user (ft/dirname dest)))
            (validators/path-not-exists cm dest))
          (let [async-task-id (async-tasks/run-async-thread
                                (new-task "data-rename" user {:source source :destination dest})
                                rename-path-thread "data-rename")]
            {:user user :source source :dest dest :async-task-id async-task-id}))))))

(defn- rename-uuid
  "Rename by UUID: given a user, a source file UUID, and a new name, rename within the same folder."
  [user source-uuid dest-base]
  (let [source (ft/rm-last-slash (uuids/path-for-uuid user source-uuid))
        src-dir (ft/dirname source)
        dest (str (ft/add-trailing-slash src-dir) dest-base)]
    (rename-path user source dest)))

(defn do-rename-uuid
  [{user :user} {dest-base :filename} source-uuid]
  (rename-uuid user source-uuid dest-base))

(defn- move-uuid
  "Rename by UUID: given a user, a source file UUID, and a new directory, move retaining the filename."
  [user source-uuid dest-dir]
  (let [source (ft/rm-last-slash (uuids/path-for-uuid user source-uuid))
        src-base (ft/basename source)
        dest (str (ft/add-trailing-slash dest-dir) src-base)]
    (rename-path user source dest)))

(defn do-move-uuid
  [{user :user} {dest-dir :dirname} source-uuid]
  (move-uuid user source-uuid dest-dir))

(defn do-move
  [{user :user} {sources :sources dest :dest}]
  (move-paths user sources dest))

(defn- move-uuid-contents
  "Rename by UUID: given a user, a source directory UUID, and a new directory, move the directory contents, retaining the filename."
  [user source-uuid dest-dir]
  (let [source (ft/rm-last-slash (uuids/path-for-uuid user source-uuid))]
    (irods/with-jargon-exceptions [cm]
      (validators/path-is-dir cm source))
    (let [sources (directory/get-paths-in-folder user source)]
      (move-paths user sources dest-dir))))

(defn do-move-uuid-contents
  [{user :user} {dest-dir :dirname} source-uuid]
  (move-uuid-contents user source-uuid dest-dir))

(with-post-hook! #'do-rename-uuid (dul/log-func "do-rename-uuid"))

(with-pre-hook! #'do-rename-uuid
  (fn [params body source-uuid]
    (dul/log-call "do-rename-uuid" params body source-uuid)))

(with-post-hook! #'do-move-uuid (dul/log-func "do-move-uuid"))

(with-pre-hook! #'do-move-uuid
  (fn [params body source-uuid]
    (dul/log-call "do-move-uuid" params body source-uuid)))

(with-post-hook! #'do-move-uuid-contents (dul/log-func "do-move-uuid-contents"))

(with-pre-hook! #'do-move-uuid-contents
  (fn [params body source-uuid]
    (dul/log-call "do-move-uuid-contents" params body source-uuid)))

(with-pre-hook! #'do-move
  (fn [params body]
    (dul/log-call "do-move" params body)))

(with-post-hook! #'do-move (dul/log-func "do-move"))
