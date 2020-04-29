(ns data-info.services.trash
  (:use [clojure-commons.error-codes]
        [clj-jargon.item-ops]
        [clj-jargon.item-info]
        [clj-jargon.metadata]
        [clj-jargon.permissions]
        [clj-jargon.tickets :exclude [iget iput]]
        [slingshot.slingshot :only [try+ throw+]])
  (:require [clojure.tools.logging :as log]
            [clojure-commons.file-utils :as ft]
            [dire.core :refer [with-pre-hook! with-post-hook!]]
            [data-info.clients.async-tasks :as async-tasks]
            [data-info.util.config :as cfg]
            [data-info.util.irods :as irods]
            [data-info.util.paths :as paths]
            [data-info.util.logging :as dul]
            [data-info.services.directory :as directory]
            [data-info.services.uuids :as uuids]
            [data-info.services.rename :as rename]
            [data-info.util.validators :as validators])
  (:import [org.irods.jargon.core.pub IRODSFileSystemAO]
           [org.irods.jargon.core.pub.io IRODSFile]))

(def ^:private trash-attr "ipc-trash-origin")

(def alphanums (concat (range 48 58) (range 65 91) (range 97 123)))

(defn- rand-str
  [length]
  (apply str (take length (repeatedly #(char (rand-nth alphanums))))))

(defn- randomized-trash-path
  [user path-to-inc]
  (ft/path-join
   (paths/user-trash-path user)
   (str (ft/basename path-to-inc) "." (rand-str 7))))

(defn- move-to-trash
  [cm p trash-path user & {:keys [update-fn] :or {update-fn (fn [_ _])}}]
  (move cm p trash-path :user user :admin-users (cfg/irods-admins) :update-fn update-fn)
  (set-metadata cm trash-path trash-attr p paths/IPCSYSTEM)
  (update-fn p :set-trash-path-metadata)
  trash-path)

(defn- home-matcher
  [user path]
  (= (str "/" (cfg/irods-zone) "/home/" user)
     (ft/rm-last-slash path)))

(defn- validate-not-homedir
  [user paths]
  (when (some true? (mapv #(home-matcher user %) paths))
    (throw+ {:error_code ERR_NOT_AUTHORIZED
             :paths (filterv #(home-matcher user %) paths)})))

(defn- delete-paths-thread
  [async-task-id]
  (let [jargon-fn (fn [cm async-task update-fn]
                    (update-fn "deleted paths" :begin)
                    (let [{:keys [username data]} async-task]
                      (log/info data)
                      (log/info (:trash-paths data))
                      (doseq [^String p (:paths data)]
                        (update-fn p :begin-delete)
                        ;;; Delete all of the tickets associated with the file.
                        (let [path-tickets (mapv :ticket-id (ticket-ids-for-path cm (:username cm) p))]
                          (doseq [path-ticket path-tickets]
                            (delete-ticket cm (:username cm) path-ticket)))

                        (update-fn p :deleted-tickets)

                        ;;; If the file isn't already in the user's trash, move it there
                        ;;; otherwise, do a hard delete.
                        (if (contains? (:trash-paths data) (keyword p))
                          (move-to-trash cm p ((:trash-paths data) (keyword p)) username :update-fn update-fn)
                          (delete cm p true)) ;;; Force a delete to bypass proxy user's trash.
                        (update-fn p :end-delete)))
                    (update-fn "deleted paths" :end))]
    (async-tasks/paths-async-thread async-task-id jargon-fn false))) ;; we don't use a client user so we can delete tickets

(defn- delete-paths
  ([user paths]
   (irods/with-jargon-exceptions [cm]
     (delete-paths cm user paths)))
  ([cm user paths]
     (let [paths (mapv ft/rm-last-slash paths)]
       (validators/user-exists cm user)
       (validators/all-paths-exist cm paths)
       (validators/user-owns-paths cm user paths)
       (validate-not-homedir user paths)

       (let [trash-paths (apply merge (mapv
                           (fn [path]
                             (if-not (.startsWith path (paths/user-trash-path user))
                               {path (randomized-trash-path user path)}
                               {}))
                           paths))
             async-task-id (async-tasks/run-async-thread
                             (rename/new-task "data-delete" user {:paths paths :trash-paths trash-paths})
                             delete-paths-thread "data-delete")]
         {:paths paths
          :trash-paths trash-paths
          :async-task-id async-task-id}))))

(defn- delete-uuid
  "Delete by UUID: given a user and a data item UUID, delete that data item, returning a list of filenames deleted."
  [user source-uuid]
  (let [path (ft/rm-last-slash (uuids/path-for-uuid user source-uuid))]
    (validate-not-homedir user [path])
    (delete-paths user [path])))

(defn- delete-uuid-contents
  "Delete contents by UUID: given a user and a data item UUID, delete the contents, returning a list of filenames deleted."
  [user source-uuid]
  (irods/with-jargon-exceptions [cm]
    (let [source (ft/rm-last-slash (uuids/path-for-uuid cm user source-uuid))]
      (validators/path-is-dir cm source)
      (let [paths (directory/get-paths-in-folder user source)]
        (delete-paths cm user paths)))))

(defn- list-in-dir
  [{^IRODSFileSystemAO cm-ao :fileSystemAO :as cm} fixed-path]
  (let [ffilter (proxy [java.io.FileFilter] [] (accept [stuff] true))]
    (.getListInDirWithFileFilter
      cm-ao
      (file cm fixed-path)
      ffilter)))

(defn- delete-trash
  "Permanently delete the contents of a user's trash directory."
  [user]
  (irods/with-jargon-exceptions [cm]
    (validators/user-exists cm user)
    (let [trash-dir  (paths/user-trash-path user)
          trash-list (mapv (fn [^IRODSFile file] (.getAbsolutePath file)) (list-in-dir cm (ft/rm-last-slash trash-dir)))]
      (doseq [trash-path trash-list]
        (delete cm trash-path true))
      {:trash trash-dir
       :paths trash-list})))

(defn- restore-to-homedir?
  "Whether to restore a given file to the home directory.

   This happens when the trash origin attribute is missing, or when the parent
   directory exists but is not writeable by the current user."
  [cm user p]
  (or (not (attribute? cm p trash-attr))
      (let [origin-parent (ft/dirname (:value (first (get-attribute cm p trash-attr))))]
        (and (exists? cm origin-parent)
             (not (is-writeable? cm user origin-parent))))))

(defn- trash-origin-path
  [cm user p restore-to-homedir]
  (if (not restore-to-homedir)
    (:value (first (get-attribute cm p trash-attr)))
    (ft/path-join (paths/user-home-dir user) (ft/basename p))))

(defn- restoration-paths
  "Given a path, return a map of the path to restore to and whether the file was returned to the home directory."
  [cm user path]
  (let [user-home          (paths/user-home-dir user)
        restore-to-homedir (restore-to-homedir? cm user path)
        origin-path        (trash-origin-path cm user path restore-to-homedir)
        inc-path           #(str origin-path "." %)]
    {:restored-path
     (ft/rm-last-slash
       (if-not (exists? cm origin-path)
         origin-path
         (loop [attempts 0]
           (if (exists? cm (inc-path attempts))
             (recur (inc attempts))
             (inc-path attempts)))))
     :partial-restore restore-to-homedir}))

(defn- find-extant-parent
  [cm path]
  (let [parent (ft/dirname path)]
    (if (exists? cm parent)
      parent
      (recur cm parent))))

(defn- restore-parent-dirs
  [cm user path]
  (log/warn "restore-parent-dirs" (ft/dirname path))

  (when-not (exists? cm (ft/dirname path))
    (let [extant-parent (find-extant-parent cm path)]
      (log/warn "Already-existing parent dir for " path " is " extant-parent)
      (mkdirs cm (ft/dirname path))
      (log/warn "Created " (ft/dirname path))

      (loop [parent (ft/dirname path)]
        (log/warn "restoring path" parent)
        (log/warn "user parent path" user)

        (when (and (not= parent extant-parent)
                   (not= parent (paths/user-home-dir user))
                   (not (owns? cm user parent)))
          (log/warn (str "Giving ownership to " user " of parent dir: " parent))
          (set-owner cm parent user)
          (recur (ft/dirname parent)))))))

(defn- restore-paths-thread
  [async-task-id]
  (let [jargon-fn (fn [cm async-task update-fn]
                    (update-fn "deleted paths" :begin)
                    (let [{:keys [username data]} async-task]
                      (doseq [^String p (:paths data)]
                        (let [fully-restored      (:restored-path ((:restoration-paths data) (keyword p)))
                              restored-to-homedir (:partial-restore ((:restoration-paths data) (keyword p)))]
                          (update-fn p :begin-restore)
                          (log/warn "Restoring " p " to " fully-restored)

                          (validators/path-not-exists cm fully-restored)
                          (log/warn fully-restored " does not exist. That's good.")

                          (restore-parent-dirs cm username fully-restored)
                          (log/warn "Done restoring parent dirs for " fully-restored)

                          (validators/path-writeable cm username (ft/dirname fully-restored))
                          (log/warn fully-restored "is writeable. That's good.")

                          (log/warn "Moving " p " to " fully-restored)
                          (validators/path-not-exists cm fully-restored)

                          (log/warn fully-restored " does not exist. That's good.")
                          (move cm p fully-restored :user username :admin-users (cfg/irods-admins) :update-fn update-fn)
                          (log/warn "Done moving " p " to " fully-restored)

                          (update-fn p :end-restore))))
                    (update-fn "deleted paths" :end))]
    (async-tasks/paths-async-thread async-task-id jargon-fn)))

(defn- restore-paths
  [{:keys [user paths user-trash]}]
  (let [paths (mapv ft/rm-last-slash paths)]
    (if (seq paths)
      (irods/with-jargon-exceptions [cm]
        (validators/user-exists cm user)
        (validators/all-paths-exist cm paths)
        (validators/all-paths-writeable cm user paths)

        (let [retval (apply merge (mapv
                                    (fn [path]
                                      {path (restoration-paths cm user path)})
                                    paths))
              async-task-id (async-tasks/run-async-thread
                              (rename/new-task "data-restore" user {:paths paths :restoration-paths retval})
                              restore-paths-thread "data-restore")]
         {:restored retval :async-task-id async-task-id})))))

(defn do-delete
  [{user :user} {paths :paths}]
  (delete-paths user paths))

(with-pre-hook! #'do-delete
  (fn [params body]
    (dul/log-call "do-delete" params body)
    (validate-not-homedir (:user params) (:paths body))))

(with-post-hook! #'do-delete (dul/log-func "do-delete"))

(defn do-delete-uuid
  [{user :user} data-id]
  (delete-uuid user data-id))

(with-pre-hook! #'do-delete-uuid
  (fn [params data-id]
    (dul/log-call "do-delete-uuid" params data-id)))

(with-post-hook! #'do-delete-uuid (dul/log-func "do-delete-uuid"))

(defn do-delete-uuid-contents
  [{user :user} data-id]
  (delete-uuid-contents user data-id))

(with-pre-hook! #'do-delete-uuid-contents
  (fn [params data-id]
    (dul/log-call "do-delete-uuid-contents" params data-id)))

(with-post-hook! #'do-delete-uuid-contents (dul/log-func "do-delete-uuid-contents"))

(defn do-delete-trash
  [{user :user}]
  (delete-trash user))

(with-post-hook! #'do-delete-trash (dul/log-func "do-delete-trash"))

(with-pre-hook! #'do-delete-trash
  (fn [params]
    (dul/log-call "do-delete-trash" params)))

(defn do-restore
  [{user :user} {paths :paths}]
  (let [trash (paths/user-trash-path user)
        paths (if (seq paths) paths (directory/get-paths-in-folder user trash))]
    (restore-paths
      {:user  user
       :paths paths
       :user-trash trash})))

(with-post-hook! #'do-restore (dul/log-func "do-restore"))

(with-pre-hook! #'do-restore
  (fn [params body]
    (dul/log-call "do-restore" params body)))
