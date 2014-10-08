(ns data-info.services.exists
  (:require [cemerick.url :as url]
            [dire.core :refer [with-pre-hook! with-post-hook!]]
            [clj-jargon.init :refer [with-jargon]]
            [clj-jargon.item-info :as item]
            [clj-jargon.permissions :as perm]
            [clojure-commons.file-utils :as ft]
            [clojure-commons.validators :as cv]
            [data-info.util.config :as cfg]
            [data-info.util.logging :as log]
            [data-info.services.uuids :as uuid]
            [data-info.services.validators :as dsv])
  (:import [java.util UUID]))


(defn- url-encoded?
  [string-to-check]
  (re-seq #"\%[A-Fa-f0-9]{2}" string-to-check))


(defn- url-decode
  [string-to-decode]
  (if (url-encoded? string-to-decode)
    (url/url-decode string-to-decode)
    string-to-decode))


(defn- path-exists-for-user?
  [user path]
  (let [path (ft/rm-last-slash (url-decode path))]
    (with-jargon (cfg/jargon-cfg) [cm]
      (dsv/user-exists cm user)
      (and (item/exists? cm path)
           (perm/is-readable? cm user path)))))


(defn do-exists
  [{user :user} {paths :paths}]
  {:paths (into {}
                (map #(hash-map % (path-exists-for-user? user %)) (set paths)))})

(with-pre-hook! #'do-exists
  (fn [params body]
    (log/log-call "do-exists" params)
    (cv/validate-map params {:user string?})
    (cv/validate-map body {:paths vector?})
    (dsv/validate-num-paths (:paths body))))

(with-post-hook! #'do-exists (log/log-func "do-exists"))


(defn exists?
  [{user :user entry :entry}]
  (with-jargon (cfg/jargon-cfg) [cm]
    (dsv/user-exists cm user)
    (if-let [path (uuid/get-path cm (UUID/fromString entry))]
      {:status (if (perm/is-readable? cm user path) 200 403)}
      {:status 404})))