(ns data-info.services.path-lists
  (:use [clojure-commons.error-codes]
        [clj-jargon.item-ops :only [copy-stream]]
        [slingshot.slingshot :only [throw+]])
  (:require [clojure.string :as string]
            [clojure-commons.file-utils :as ft]
            [clj-icat-direct.icat :as icat]
            [clj-irods.core :as rods]
            [clj-irods.validate :refer [validate]]
            [clj-jargon.permissions :as perms]
            [data-info.services.filetypes :as filetypes]
            [data-info.services.stat :as stat]
            [data-info.services.stat.common :refer [process-filters]]
            [data-info.services.stat.jargon :as jargon-stat]
            [data-info.util.config :as cfg]
            [data-info.util.irods :as irods]
            [data-info.util.validators :as validators]))

(defn- stat-is-dir?
  [{:keys [type]}]
  (= :dir type))

(defn- fmt-entry
  [{:keys [uuid type full_path base_name info_type access_type_id]}]
  {:id         uuid
   :type       (case type "dataobject" :file "collection" :dir)
   :path       full_path
   :label      base_name
   :infoType   info_type
   :permission (perms/fmt-perm access_type_id)})

(defn- filter-entity-type
  [recursive? folders-only?]
  (cond
    (and recursive? (not folders-only?)) :any
    folders-only?                        :folder
    :else                                :file))

(defn- folder-listing
  "Fetches a folder's contents, listing files, folders, or both depending on the given parameters."
  [user info-types folders-only? recursive? path]
  (map fmt-entry
       (icat/paged-folder-listing
        :user           user
        :zone           (cfg/irods-zone)
        :folder-path    path
        :info-types     info-types
        :entity-type    (filter-entity-type recursive? folders-only?)
        :sort-column    :full-path
        :sort-direction :asc
        :limit          nil
        :offset         0)))

(defn- list-item-with-subitems
  [user info-types folders-only? recursive? {:keys [path] :as data-item}]
  "If given a file, returns that file as the only item in a list.
   If given a folder, returns that folder in a list, and the list may include the folder's subfolder/files
   depending on the given parameters."
  (let [sub-listing (when (and recursive? (stat-is-dir? data-item))
                      (mapcat (partial list-item-with-subitems user info-types folders-only? recursive?)
                              (folder-listing user info-types folders-only? recursive? path)))]
    (concat [data-item] sub-listing)))

(defn- keep-top-level-file?
  "A filter predicate to keep only files with an info-type that matches one in the given `info-types` list,
   or to keep all files if `info-types` is empty."
  [info-types]
  (if (empty? info-types)
    (constantly true)
    (comp (partial some (set info-types)) list :infoType)))

(defn- label-matches?
  "Returns true if the given `name-pattern` matches the given label, or if the given `name-pattern` is blank."
  [name-pattern label]
  (or (string/blank? name-pattern)
      (re-find (re-pattern name-pattern) label)))

(defn- keep-data-item?
  "Returns true if the given `data-item` has permissions set,
   its label passes the `label-matches?` check,
   and if it's a file or folder depending on the given `folders-only?` flag."
  [name-pattern folders-only? {:keys [label permission] :as data-item}]
  (and permission
       (label-matches? name-pattern label)
       (if (stat-is-dir? data-item)
         folders-only?
         (not folders-only?))))

(defn- get-top-level-file-stats
  [irods user path]
  (stat/path-stat irods user path :filter-include [:path :infoType :label :permission :type]))

(defn- paths->path-list
  "Filters the given paths and returns a string of these paths appended to an HT Path List header.
   Throws an error if the filtering params result in no matching paths."
  [irods user path-list-file-identifier name-pattern info-types folders-only? recursive? paths]
  (let [zone-from-path (fn [path] (first (remove empty? (string/split path #"/"))))
        {folder-paths true file-paths false} (group-by #(= @(rods/object-type irods user (zone-from-path %) %) :dir) paths)
        files          (->> file-paths
                            (map (partial get-top-level-file-stats irods user))
                            (filter (keep-top-level-file? info-types)))
        folders        (->> folder-paths
                            (mapcat (partial folder-listing user info-types folders-only? recursive?))
                            (mapcat (partial list-item-with-subitems user info-types folders-only? recursive?)))
        filtered-paths (->> (concat files folders)
                            (filter (partial keep-data-item? name-pattern folders-only?))
                            (map :path))]

    (when (empty? filtered-paths)
      (throw+ {:error_code ERR_NOT_FOUND
               :reason "No paths matched the request."}))

    (string/join "\n" (concat [path-list-file-identifier] filtered-paths))))

(defn- info-type->file-identifier
  "Returns the appropriate Path List file identifier for the given `path-list-info-type`."
  [path-list-info-type]
  (if (= path-list-info-type (cfg/ht-path-list-info-type))
    (cfg/ht-path-list-file-identifier)
    (cfg/multi-input-path-list-identifier)))

(defn- validate-request-paths
  [irods user dest paths]
  (let [dest-dir (ft/dirname dest)
        zone     (first (remove empty? (string/split dest-dir #"/")))]
    (validate irods
              [:path-exists dest-dir user zone]
              [:path-writeable dest-dir user zone]
              [:path-not-exists dest user zone]
              [:path-exists paths user zone])
    (doseq [path paths]
      (validators/not-base-path user path))))

(defn create-path-list
  "Creates an HT Path List file from the given set of paths and filtering params.
   The resulting HT Path List file will contain only file or only folder paths (depending on the `folders-only` param),
   but no paths of folders in the initial given list will be included.
   If `recursive` is true, then all subfolders (plus all their files and subfolders)
   of any given folder paths are parsed and filtered as well.
   Throws an error if the filtering params result in no matching paths."
  [{:keys [user dest path-list-info-type name-pattern info-type folders-only recursive]
    :or   {path-list-info-type (cfg/ht-path-list-info-type)}}
   {:keys [paths]}]
  (irods/with-irods-exceptions {:jargon-opts {:client-user user}} irods
    (validate-request-paths irods user dest paths)

    (let [path-list-contents  (paths->path-list irods
                                                user
                                                (info-type->file-identifier path-list-info-type)
                                                name-pattern
                                                info-type
                                                folders-only
                                                recursive
                                                paths)
          path-list-file-stat (with-in-str path-list-contents (copy-stream @(:jargon irods) *in* user dest))]
      (irods/with-jargon-exceptions [admin-cm]
        (filetypes/add-type-to-validated-path admin-cm dest path-list-info-type))
      (rods/invalidate irods dest)
      {:file (stat/decorate-stat irods user (cfg/irods-zone) path-list-file-stat (process-filters nil nil))})))
