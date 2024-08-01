(ns data-info.services.stat.jargon
  (:require [clj-icat-direct.icat :as icat]
            [clj-jargon.by-uuid :as uuid]
            [clj-jargon.item-info :as info]
            [clj-jargon.metadata :as meta]
            [clj-jargon.permissions :as perm]
            [clojure.tools.logging :as log]
            [clojure-commons.file-utils :as ft]
            [data-info.services.stat.common
             :refer [is-dir? owns? needs-key? needs-any-key? assoc-if-selected merge-label process-filters]]
            [data-info.services.uuids :as uuids]
            [data-info.util.config :as cfg]
            [data-info.util.irods :as irods]
            [data-info.util.validators :as validators]))

(defn- get-types
  "Gets the file type associated with path."
  [cm user path & {:keys [validate?] :or {validate? true}}]
  (when validate?
    (validators/path-exists cm path)
    (validators/user-exists cm user)
    (validators/path-readable cm user path))
  (let [path-types (meta/get-attribute cm path (cfg/type-detect-type-attribute))]
    (log/info "Retrieved types" path-types "from" path "for" (str user "."))
    (or (:value (first path-types) ""))))

(defn- merge-type-info
  [stat-map cm user path included-keys & {:keys [validate?] :or {validate? true}}]
  (if (and (needs-any-key? included-keys :infoType :content-type) (not (is-dir? stat-map)))
    (assoc-if-selected stat-map included-keys
                       :infoType     (get-types cm user path :validate? validate?)
                       :content-type (irods/detect-media-type cm path))
    stat-map))

(defn- count-shares
  [cm user path]
  (let [filter-users (set (conj (cfg/perms-filter) user (cfg/irods-user)))
        other-perm?  (fn [perm] (not (contains? filter-users (:user perm))))]
    (count (filterv other-perm? (perm/list-user-perms cm path)))))

(defn- merge-shares
  [stat-map cm user path included-keys]
  (if (and (needs-key? included-keys :share-count) (owns? stat-map))
    (assoc stat-map :share-count (count-shares cm user path))
    stat-map))

(defn- merge-counts
  [stat-map cm user path included-keys]
  (if (and (needs-any-key? included-keys :file-count :dir-count) (is-dir? stat-map))
    (assoc-if-selected stat-map included-keys
                       :file-count (icat/number-of-files-in-folder user (cfg/irods-zone) path)
                       :dir-count  (icat/number-of-folders-in-folder user (cfg/irods-zone) path))
    stat-map))

(defn decorate-stat
  [cm user {:keys [path] :as stat} included-keys & {:keys [validate?] :or {validate? true}}]
  (-> stat
      (assoc-if-selected included-keys
                         :id         (-> (meta/get-attribute cm path uuid/uuid-attr) first :value)
                         :permission (perm/permission-for cm user path))
      (merge-label user path included-keys)
      (merge-type-info cm user path included-keys :validate? validate?)
      (merge-shares cm user path included-keys)
      (merge-counts cm user path included-keys)
      (select-keys included-keys)))

(defn path-stat
  [cm user path & {:keys [filter-include filter-exclude validate?]
                   :or   {filter-include nil filter-exclude nil validate? true}}]
  (let [path          (ft/rm-last-slash path)
        included-keys (process-filters filter-include filter-exclude)]
    (log/debug "[path-stat] user:" user "path:" path)
    (when validate? (validators/path-exists cm path))
    (let [base-stat (if (needs-any-key? included-keys :type :date-created :date-modified :file-size :md5)
                      (info/stat cm path)
                      {:path path})]
      (decorate-stat cm user base-stat included-keys :validate? validate?))))

(defn uuid-stat
  [cm user uuid & {:keys [filter-include filter-exclude] :or {filter-include nil filter-exclude nil}}]
  (log/debug "[uuid-stat] user:" user "uuid:" uuid)
  (let [path (uuids/path-for-uuid cm user uuid)]
    (path-stat cm user path :filter-include filter-include :filter-exclude filter-exclude)))
