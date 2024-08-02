(ns data-info.services.uuids
  (:use [clj-jargon.permissions]
        [slingshot.slingshot :only [throw+]])
  (:require [clojure.tools.logging :as log]
            [clj-icat-direct.icat :as icat]
            [clj-jargon.by-uuid :as uuid]
            [clj-irods.core :as rods]
            [clj-irods.validate :refer [validate]]
            [clojure-commons.error-codes :as error]
            [data-info.util.irods :as irods]
            [data-info.util.config :as cfg])
  (:import [java.util UUID]
           [clojure.lang IPersistentMap]))


(defn path-for-uuid
  "Resolves a path for the entity with a given UUID.

   Params:
     user - the user requesting the info
     uuid - the UUID

   Returns:
     It returns a path."
  ([^IPersistentMap cm ^String user ^UUID uuid]
   (if-let [path (uuid/get-path cm uuid)]
     path
     (throw+ {:error_code error/ERR_DOES_NOT_EXIST :uuid uuid})))
  ([^String user ^UUID uuid]
   (irods/with-jargon-exceptions [cm]
     (path-for-uuid cm user uuid))))

(defn do-simple-uuid-for-path
  [{:keys [user path]}]
  (irods/with-irods-exceptions {:use-icat-transaction false} irods
    (validate irods
              [:user-exists user (cfg/irods-zone)]
              [:path-exists path user (cfg/irods-zone)]
              [:path-readable path user (cfg/irods-zone)])
    {:id @(rods/uuid irods user (cfg/irods-zone) path)}))
