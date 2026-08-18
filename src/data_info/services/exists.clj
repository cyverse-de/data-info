(ns data-info.services.exists
  (:require [dire.core :refer [with-pre-hook! with-post-hook!]]
            [clj-jargon.item-info :as item]
            [clj-jargon.permissions :as perm]
            [clojure-commons.file-utils :as ft]
            [data-info.util.irods :as irods]
            [data-info.util.logging :as log]
            [data-info.util.validators :as duv]))

(defn- path-exists-for-user?
  [cm user path]
  (let [path (ft/rm-last-slash path)]
    (and (item/exists? cm path)
         (perm/is-readable? cm user path))))

(defn do-exists
  [{user :user} {paths :paths}]
  (irods/with-jargon-exceptions [cm]
    (duv/user-exists cm user)
    {:paths (into {} (map (juxt keyword (partial path-exists-for-user? cm user)) (set paths)))}))

(with-pre-hook! #'do-exists
  (fn [params body]
    (log/log-call "do-exists" params)
    (duv/validate-num-paths (:paths body))))

(with-post-hook! #'do-exists (log/log-func "do-exists"))

(defn- ancestors-of
  "Returns path followed by each of its ancestors, ending at the root."
  [path]
  (take-while some? (iterate ft/dirname path)))

(defn- deepest-extant-ancestor
  "Returns the deepest ancestor of path that exists in iRODS, including path itself."
  [cm path]
  (first (filter (partial item/exists? cm) (ancestors-of path))))

(defn- path-creatable-for-user?
  "Indicates whether a folder could be created at path. Any ancestors that don't exist yet are
   assumed to be created along with it, so the question is whether the deepest ancestor that does
   exist is a folder the user can write to."
  [cm user path]
  (let [path (ft/rm-last-slash path)]
    (boolean
     (when-let [ancestor (deepest-extant-ancestor cm path)]
       (and (item/is-dir? cm ancestor)
            (perm/is-writeable? cm user ancestor))))))

(defn do-creatability
  [{user :user} {paths :paths}]
  (irods/with-jargon-exceptions [cm]
    (duv/user-exists cm user)
    {:paths (into {} (map (juxt keyword (partial path-creatable-for-user? cm user)) (set paths)))}))

(with-pre-hook! #'do-creatability
  (fn [params body]
    (log/log-call "do-creatability" params)
    (duv/validate-num-paths (:paths body))))

(with-post-hook! #'do-creatability (log/log-func "do-creatability"))
