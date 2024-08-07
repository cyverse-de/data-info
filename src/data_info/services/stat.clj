(ns data-info.services.stat
  (:require [dire.core :refer [with-pre-hook! with-post-hook!]]
            [clj-irods.core :as rods]
            [clj-irods.validate :refer [validate]]
            [clojure-commons.file-utils :as ft]
            [data-info.services.stat.common
             :refer [is-dir? owns? needs-key? needs-any-key? assoc-if-selected merge-label process-filters]]
            [data-info.util.config :as cfg]
            [data-info.util.logging :as dul]
            [data-info.util.irods :as irods]
            [data-info.util.validators :as validators])
  (:import [clojure.lang IPersistentMap]))

(defn- get-types
  "Gets the file type associated with the path."
  [irods user zone path & {:keys [validate?] :or {validate? true}}]
  (when validate?
    (validate irods
              [:path-exists path user zone]
              [:user-exists user zone]
              [:path-readable path user zone]))
  @(rods/info-type irods user zone path))

(defn- count-shares
  [irods user path]
  (let [user-filter (set (conj (cfg/perms-filter) user (cfg/irods-user)))]
    (count (remove (comp user-filter :user) @(rods/list-user-permissions irods path)))))

(defn- merge-counts
  [stat-map irods user zone path included-keys]
  (if (and (needs-any-key? included-keys :file-count :dir-count) (is-dir? stat-map))
    (assoc-if-selected stat-map included-keys
                       :file-count @(rods/number-of-files-in-folder irods user zone path)
                       :dir-count  @(rods/number-of-folders-in-folder irods user zone path))
    stat-map))

(defn- merge-shares
  [stat-map irods user path included-keys]
  (if (and (needs-key? included-keys :share-count) (owns? stat-map))
    (assoc stat-map :share-count (count-shares irods user path))
    stat-map))

(defn- merge-type-info
  [stat-map irods user zone path included-keys & {:keys [validate?] :or {validate? true}}]
  (if (and (needs-any-key? included-keys :infoType :content-type) (not (is-dir? stat-map)))
    (assoc-if-selected stat-map included-keys
                       :infoType     (get-types irods user zone path :validate? validate?)
                       :content-type (irods/detect-media-type @(:jargon irods) path))
    stat-map))

(defn decorate-stat
  [irods user zone {:keys [path] :as stat} included-keys & {:keys [validate?] :or {validate? true}}]
  (-> stat
      (assoc-if-selected included-keys
                         :id         @(rods/uuid irods user zone path)
                         :permission @(rods/permission irods user zone path))
      (merge-label user path included-keys)
      (merge-type-info irods user zone path included-keys :validate? validate?)
      (merge-shares irods user path included-keys)
      (merge-counts irods user zone path included-keys)
      (select-keys included-keys)))

(defn path-stat
  [irods user path & {:keys [filter-include filter-exclude validate?]
                      :or   {filter-include nil filter-exclude nil validate? true}}]
  (let [path          (ft/rm-last-slash path)
        included-keys (process-filters filter-include filter-exclude)]
    (when validate? (validate irods [:path-exists path user (cfg/irods-zone)]))
    (let [base-stat (if (needs-any-key? included-keys :type :date-created :date-modified :file-size :md5)
                      @(rods/stat irods user (cfg/irods-zone) path)
                      {:path path})]
      (decorate-stat irods user (cfg/irods-zone) base-stat included-keys :validate? validate?))))

(defn- remove-missing-paths
  "Removes non-existent paths from a list of item paths."
  [irods user zone paths]
  (let [path-missing? (fn [p] (or (nil? p) (= @(rods/object-type irods user zone p) :none)))]
    (remove path-missing? paths)))

(defn- check-stat-permissions
  "Validates the permissions on all items that a user is requesting stat information for."
  [irods user validation-behavior paths]
  (let [validator-for     {:own :path-owned :write :path-writeable :read :path-readable}
        default-validator :path-readable
        validator         (get validator-for (keyword validation-behavior) default-validator)]
    (validate irods [validator paths user (cfg/irods-zone)])))

(defn- acceptable-permissions-for
  "Returns the set of acceptable permissions for a given validation behavior."
  [validation-behavior]
  (case (keyword validation-behavior)
    :own   #{:own}
    :write #{:own :write}
    #{:own :write :read}))

(defn- remove-inaccessible-paths
  "Removes entries from a list of paths that the user cannot access."
  [irods user validation-behavior paths]
  (let [acceptable-permissions (acceptable-permissions-for validation-behavior)]
    (filter
     (fn [p] (get acceptable-permissions @(rods/permission irods user (cfg/irods-zone) p)))
     paths)))

(defn do-stat
  [{:keys [user validation-behavior filter-include filter-exclude ignore-missing ignore-inaccessible]
    :or   {filter-include nil filter-exclude nil ignore-missing false ignore-inaccessible false}}
   {paths :paths uuids :ids}]
  (irods/with-irods-exceptions {} irods
    (validate irods
              [:user-exists user (cfg/irods-zone)]
              (when-not ignore-missing [:uuid-exists (remove nil? uuids)])
              (when-not ignore-missing [:path-exists (remove nil? paths) (cfg/irods-user) (cfg/irods-zone)]))
    (let [uuid-paths       @(rods/uuids->paths irods uuids)
          all-paths        (into paths (remove nil? (map second uuid-paths)))
          extant-paths     (remove-missing-paths irods user (cfg/irods-zone) all-paths)
          accessible-paths (if ignore-inaccessible
                             (remove-inaccessible-paths irods user validation-behavior extant-paths)
                             (do (check-stat-permissions irods user validation-behavior extant-paths) extant-paths))
          uuid-paths       (filter (comp (set accessible-paths) second) uuid-paths)
          paths            (filter (set accessible-paths) paths)
          format-stat      (fn [path]
                             (path-stat irods user path
                                        :filter-include filter-include
                                        :filter-exclude filter-exclude))]
      {:paths (into {} (map (juxt keyword format-stat) paths))
       :ids   (into {} (map (juxt (comp keyword str first) (comp format-stat second)) uuid-paths))})))

(with-pre-hook! #'do-stat
  (fn [params body]
    (dul/log-call "do-stat" params body)
    (validators/validate-num-paths (:paths body))
    (validators/validate-num-paths (:ids body))))

(with-post-hook! #'do-stat (dul/log-func "do-stat"))
