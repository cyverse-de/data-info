(ns data-info.services.root
  (:use [clj-jargon.item-info :only [exists?]]
        [clj-jargon.permissions :only [set-permission owns?]])
  (:require [clojure.tools.logging :as log]
            [clj-jargon.item-info :as item]
            [clj-jargon.item-ops :as ops]
            [clj-irods.core :as rods]
            [clj-irods.validate :refer [validate]]
            [clojure-commons.file-utils :as ft]
            [data-info.services.stat :as stat]
            [data-info.util.config :as cfg]
            [data-info.util.irods :as irods]
            [data-info.util.logging :as dul]
            [data-info.util.paths :as paths]
            [data-info.util.validators :as validators]
            [dire.core :refer [with-pre-hook! with-post-hook!]]))

(defn- get-base-paths
  [user]
  {:user_home_path  (paths/user-home-dir user)
   :user_trash_path (paths/user-trash-path user)
   :base_trash_path (paths/base-trash-path)})

(defn- get-root
  [irods user root-path]
  (validate irods [:path-readable root-path user (cfg/irods-zone)]) ;; CORE-7638; otherwise a 'nil' permission can pop up and cause issues
  {:id @(rods/uuid irods user (cfg/irods-zone) root-path)
   :path root-path
   :label (paths/path->label user root-path)
   :date-created @(rods/date-created irods user (cfg/irods-zone) root-path)
   :date-modified @(rods/date-modified irods user (cfg/irods-zone) root-path)
   :permission @(rods/permission irods user (cfg/irods-zone) root-path)})

(defn- make-root
  [irods user root-path]
  (when (= @(rods/object-type irods user (cfg/irods-zone) root-path) :none)
    (log/info "[make-root] Creating" root-path "for" user)
    (ops/mkdirs @(:jargon irods) root-path))

  (when-not (= @(rods/permission irods user (cfg/irods-zone) root-path) :own)
    (log/info "[make-root] Setting own permissions on" root-path "for" user)
    (set-permission @(:jargon irods) user root-path :own))

  (get-root irods user root-path))

(defn root-listing
  [user]
  (let [{home-path :user_home_path trash-path :user_trash_path :as base-paths} (get-base-paths user)
        community-data (ft/rm-last-slash (cfg/community-data))
        irods-home     (ft/rm-last-slash (cfg/irods-home))]
    (log/debug "[root-listing]" "for" user)
    (irods/with-irods-exceptions {:use-icat-transaction false} irods
      (validate irods [:user-exists user (cfg/irods-zone)])
      {:roots (remove nil?
                [(get-root irods user home-path)
                 (get-root irods user community-data)
                 (get-root irods user irods-home)
                 (make-root irods user trash-path)])
       :base-paths base-paths})))

(defn do-root-listing
  [user]
  (root-listing user))

(with-pre-hook! #'do-root-listing
  (fn [user]
    (dul/log-call "do-root-listing" user)))

(with-post-hook! #'do-root-listing (dul/log-func "do-root-listing"))

(defn user-base-paths
  [user]
  (irods/with-jargon-exceptions [cm]
    (validators/user-exists cm user)
    (get-base-paths user)))
