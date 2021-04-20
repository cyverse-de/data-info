(ns data-info.services.directory
  (:require [dire.core :refer [with-pre-hook! with-post-hook!]]
            [clj-icat-direct.icat :as icat]
            [clj-irods.core :as rods]
            [clj-irods.validate :refer [validate]]
            [clj-jargon.permissions :as perm]
            [data-info.services.stat :as stat]
            [data-info.util.config :as cfg]
            [data-info.util.logging :as dul]
            [data-info.util.irods :as irods]
            [data-info.util.paths :as paths])
  (:import [clojure.lang ISeq]))


(defn ^ISeq get-paths-in-folder
  "Returns all of the paths of the members of a given folder that are visible to a given user.

   Parameters:
     user   - the username of the user
     folder - the folder to inspect
     limit  - (OPTIONAL) if provided, only the first <limit> members will be returned.

   Returns:
     It returns a list of paths."
  ([^String user ^String folder]
   (icat/folder-path-listing user (cfg/irods-zone) folder))

  ([^String user ^String folder ^Integer limit]
    (let [listing (icat/paged-folder-listing
                    :user           user
                    :zone           (cfg/irods-zone)
                    :folder-path    folder
                    :entity-type    :any
                    :sort-column    :base-name
                    :sort-direction :asc
                    :limit          limit
                    :offset         0
                    :info-types     nil)]
     (map :full_path listing))))


(defn- fmt-folder
  [user {:keys [full_path modify_ts create_ts access_type_id uuid]}]
  {:id           uuid
   :path         full_path
   :label        (paths/path->label user full_path)
   :permission   (perm/fmt-perm access_type_id)
   :date-created  (* (Integer/parseInt create_ts) 1000)
   :date-modified (* (Integer/parseInt modify_ts) 1000)})


(defn- list-directories
  "Lists the directories contained under path."
  [user zone path]
  (irods/with-irods-exceptions {} irods
    (validate irods
              [:user-exists user zone]
              [:path-exists path user zone]
              [:path-readable path user zone]
              [:path-is-dir path user zone])
    (-> (stat/new-path-stat irods user path :filter-include [:id :label :path :date-created :date-modified :permission])
        (assoc :folders (map (partial fmt-folder user)
                             (icat/list-folders-in-folder user (cfg/irods-zone) path))))))


(defn do-directory
  [zone path-in-zone {user :user}]
  {:folder (list-directories user (cfg/irods-zone) (irods/abs-path zone path-in-zone))})

(with-pre-hook! #'do-directory
  (fn [zone path params]
    (dul/log-call "do-directory" zone path params)))

(with-post-hook! #'do-directory (dul/log-func "do-directory"))
