(ns data-info.util.paths
  (:require [clojure-commons.file-utils :as ft]
            [clj-jargon.item-info :as item]
            [data-info.util.config :as cfg]))


(def IPCRESERVED "ipc-reserved-unit")
(def IPCSYSTEM "ipc-system-avu")


(defn ^String user-home-dir
  [^String user]
  (ft/path-join (cfg/irods-home) user))


(defn ^String base-trash-path
  []
  (item/trash-base-dir (cfg/irods-zone) (cfg/irods-user)))


(defn ^String user-trash-path
  [^String user]
  (ft/path-join (base-trash-path) user))


(defn ^Boolean in-trash?
  [^String user ^String fpath]
  (.startsWith fpath (base-trash-path)))

(defn- dir-equal?
  [path comparison]
  (apply = (map ft/rm-last-slash [path comparison])))

(defn user-trash-dir?
  [user path]
  (dir-equal? path (user-trash-path user)))

(defn base-trash-path? [path] (dir-equal? path (base-trash-path)))
(defn sharing? [path] (dir-equal? path (cfg/irods-home)))
(defn community? [path] (dir-equal? path (cfg/community-data)))

(defn path->label
  "Generates a label given an absolute path in iRODS."
  [user path]
  (cond
    (user-trash-dir? user path) "Trash"
    (sharing? path)             "Shared With Me"
    (community? path)           "Community Data"
    :else                       (ft/basename path)))
