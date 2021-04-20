(ns data-info.services.stat
  (:require [clojure.string :as string]
            [clojure.set :as cset]
            [clojure.tools.logging :as log]
            [dire.core :refer [with-pre-hook! with-post-hook!]]
            [slingshot.slingshot :refer [throw+]]
            [otel.otel :as otel]
            [clj-icat-direct.icat :as icat]
            [clj-irods.core :as rods]
            [clj-irods.validate :refer [validate]]
            [clj-jargon.by-uuid :as uuid]
            [clj-jargon.item-info :as info]
            [clj-jargon.metadata :as meta]
            [clj-jargon.permissions :as perm]
            [clojure-commons.file-utils :as ft]
            [common-swagger-api.schema.stats :refer [AvailableStatFields]]
            [data-info.util.config :as cfg]
            [data-info.util.logging :as dul]
            [data-info.util.irods :as irods]
            [data-info.util.paths :as paths]
            [data-info.services.uuids :as uuids]
            [data-info.util.validators :as validators])
  (:import [clojure.lang IPersistentMap]))


(defn- is-dir?
  [stat-map]
  (= (:type stat-map) :dir))

(defn- owns?
  [stat-map]
  (= (:permission stat-map) :own))

(defn- needs-key?
  [requested-keys needed-key?]
  (or (contains? requested-keys needed-key?)
    (case needed-key?
      ; :permission is needed by anything which uses `owns?`
      :permission (contains? requested-keys :share-count)
      ; :type is needed by anything which uses `is-dir?`
      :type       (not (empty? (cset/intersection #{:infoType :content-type :file-count :dir-count} requested-keys)))
      false)))

(defn- needs-any-key?
  [included-keys & needed-keys]
  (some #(needs-key? included-keys %) needed-keys))

(defmacro assoc-if-selected
  "Adds keys and values to a map. If the key is in the set of requested keys then the value
   is associated with the map. Otherwise, nil is associated with the map and the expression
   used to calculate the value is not executed."
  [m requested-keys & kvs]
  (let [format-kv (fn [[k v]] [k `(when (needs-key? ~requested-keys ~k) ~v)])]
    (concat `(assoc ~m) (mapcat format-kv (partition 2 kvs)))))

(defn- new-get-types
  "Gets the file type associated with the path."
  [irods user zone path & {:keys [validate?] :or {validate? true}}]
  (when validate?
    (validate irods
              [:path-exists path user zone]
              [:user-exists user zone]
              [:path-readable path user zone]))
  @(rods/info-type irods user zone path))

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

(defn- new-count-shares
  [irods user path]
  (let [user-filter (set (conj (cfg/perms-filter) user (cfg/irods-user)))]
    (count (remove (comp user-filter :user) @(rods/list-user-permissions irods path)))))

(defn- count-shares
  [cm user path]
  (let [filter-users (set (conj (cfg/perms-filter) user (cfg/irods-user)))
        other-perm?  (fn [perm] (not (contains? filter-users (:user perm))))]
    (count (filterv other-perm? (perm/list-user-perms cm path)))))

(defn- new-merge-counts
  [stat-map irods user zone path included-keys]
  (if (and (needs-any-key? included-keys :file-count :dir-count) (is-dir? stat-map))
    (otel/with-span [s ["new-merge-counts"]]
      (assoc-if-selected stat-map included-keys
        :file-count @(rods/number-of-files-in-folder irods user zone path)
        :dir-count  @(rods/number-of-folders-in-folder irods user zone path)))
    stat-map))

(defn- merge-counts
  [stat-map cm user path included-keys]
  (if (and (needs-any-key? included-keys :file-count :dir-count) (is-dir? stat-map))
    (otel/with-span [s ["merge-counts"]]
      (assoc stat-map
        :file-count (when (needs-key? included-keys :file-count) (icat/number-of-files-in-folder user (cfg/irods-zone) path))
        :dir-count  (when (needs-key? included-keys :dir-count)  (icat/number-of-folders-in-folder user (cfg/irods-zone) path))))
    stat-map))

(defn- new-merge-shares
  [stat-map irods user path included-keys]
  (if (and (needs-key? included-keys :share-count) (owns? stat-map))
    (otel/with-span [s ["new-merge-shares"]]
      (assoc stat-map :share-count (new-count-shares irods user path)))
    stat-map))

(defn- merge-shares
  [stat-map cm user path included-keys]
  (if (and (needs-key? included-keys :share-count) (owns? stat-map))
    (otel/with-span [s ["merge-shares"]]
      (assoc stat-map :share-count (count-shares cm user path)))
    stat-map))

(defn- merge-label
  [stat-map user path included-keys]
  (if (needs-key? included-keys :label)
    (assoc stat-map
           :label (paths/path->label user path))
    stat-map))

(defn- new-merge-type-info
  [stat-map irods user zone path included-keys & {:keys [validate?] :or {validate? true}}]
  (if (and (needs-any-key? included-keys :infoType :content-type) (not (is-dir? stat-map)))
    (otel/with-span [s ["new-merge-type-info"]]
      (assoc-if-selected stat-map included-keys
        :infoType     (new-get-types irods user zone path :validate? validate?)
        :content-type (irods/detect-media-type @(:jargon irods) path)))
    stat-map))

(defn- merge-type-info
  [stat-map cm user path included-keys & {:keys [validate?] :or {validate? true}}]
  (if (and (needs-any-key? included-keys :infoType :content-type) (not (is-dir? stat-map)))
    (otel/with-span [s ["merge-type-info"]]
      (assoc stat-map
        :infoType     (when (needs-key? included-keys :infoType) (get-types cm user path :validate? validate?))
        :content-type (when (needs-key? included-keys :content-type) (irods/detect-media-type cm path))))
    stat-map))

(defn new-decorate-stat
  [irods user zone {:keys [path] :as stat} included-keys & {:keys [validate?] :or {validate? true}}]
  (otel/with-span [s ["new-decorate-stat"]]
    (-> stat
        (assoc-if-selected included-keys
          :id         @(rods/uuid irods user zone path)
          :permission @(rods/permission irods user zone path))
        (merge-label user path included-keys)
        (new-merge-type-info irods user zone path included-keys :validate? validate?)
        (new-merge-shares irods user path included-keys)
        (new-merge-counts irods user zone path included-keys)
        (select-keys included-keys))))

(defn ^IPersistentMap decorate-stat
  [^IPersistentMap cm ^String user ^IPersistentMap stat included-keys & {:keys [validate?] :or {validate? true}}]
  (otel/with-span [s ["decorate-stat"]]
    (let [path (:path stat)]
      (-> stat
        (assoc :id         (when (needs-key? included-keys :id) (-> (meta/get-attribute cm path uuid/uuid-attr) first :value))
               :permission (when (needs-key? included-keys :permission) (perm/permission-for cm user path)))
        (merge-label user path included-keys)
        (merge-type-info cm user path included-keys :validate? validate?)
        (merge-shares cm user path included-keys)
        (merge-counts cm user path included-keys)
        (select-keys included-keys)))))

(defn- get-filter-set
  [filter-vec-or-string default]
  (if (nil? filter-vec-or-string)
    (set default)
    (if (string? filter-vec-or-string)
      (set (map keyword (string/split filter-vec-or-string #",")))
      (set (map keyword filter-vec-or-string)))))

(defn process-filters
  "Process an include and an exclude string into just a list of keys to include"
  [include exclude]
  (let [all-keys (set AvailableStatFields)
        includes-set (get-filter-set include all-keys)
        excludes-set (get-filter-set exclude [])]
      (cset/intersection all-keys (cset/difference includes-set excludes-set))))

(defn new-path-stat
  [irods user path & {:keys [filter-include filter-exclude validate?]
                      :or   {filter-include nil filter-exclude nil validate? true}}]
  (otel/with-span [s ["new-path-stat" {:attributes {"path" path}}]]
    (let [path          (ft/rm-last-slash path)
          included-keys (process-filters filter-include filter-exclude)]
      (when validate? (validate irods [:path-exists path user (cfg/irods-zone)]))
      (let [base-stat (if (needs-any-key? included-keys :type :date-created :date-modified :file-size :md5)
                        @(rods/stat irods user (cfg/irods-zone) path)
                        {:path path})]
        (new-decorate-stat irods user (cfg/irods-zone) base-stat included-keys :validate? validate?)))))

(defn ^IPersistentMap path-stat
  [^IPersistentMap cm ^String user ^String path & {:keys [filter-include filter-exclude validate?] :or {filter-include nil filter-exclude nil validate? true}}]
  (otel/with-span [s ["path-stat" {:attributes {"path" path}}]]
    (let [path (ft/rm-last-slash path)
          included-keys (process-filters filter-include filter-exclude)]
      (log/debug "[path-stat] user:" user "path:" path)
      (when validate? (validators/path-exists cm path))
      (let [base-stat (if (needs-any-key? included-keys :type :date-created :date-modified :file-size :md5)
                        (info/stat cm path)
                        {:path path})]
        (decorate-stat cm user base-stat included-keys :validate? validate?)))))

(defn ^IPersistentMap uuid-stat
  [^IPersistentMap cm ^String user uuid & {:keys [filter-include filter-exclude] :or {filter-include nil filter-exclude nil}}]
  (otel/with-span [s ["uuid-stat"]]
    (log/debug "[uuid-stat] user:" user "uuid:" uuid)
    (let [path (uuids/path-for-uuid cm user uuid)]
      (path-stat cm user path :filter-include filter-include :filter-exclude filter-exclude))))

(defn- get-uuid-paths
  "Returns a sequence of vectors containing the UUID of a file or folder and its path. UUIDs that can't be
   found are simply ignored"
  [cm uuids]
  (->> (map (juxt (comp keyword str) (partial uuid/get-path cm)) uuids)
       (remove (comp nil? second))))

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
                             ;; TODO: update this to use clj-irods
                             (new-path-stat irods user path
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
