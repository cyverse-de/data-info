(ns data-info.services.entry
  "This namespace provides the business logic for all entries endpoints."
  (:use [slingshot.slingshot :only [throw+]])
  (:require [me.raynes.fs :as fs]
            [clj-icat-direct.icat :as icat]
            [clojure.java.io :as io]
            [clj-jargon.init :refer [proxy-input-stream-return clean-return]]
            [clj-irods.core :as rods]
            [clj-jargon.by-uuid :as uuid]
            [clj-jargon.item-info :as item]
            [clj-jargon.item-ops :as ops]
            [clj-jargon.permissions :as perm]
            [clj-jargon.users :as user]
            [clj-jargon.validations :as jv]
            [clojure-commons.error-codes :as error]
            [clojure-commons.file-utils :as file]
            [data-info.util.config :as cfg]
            [data-info.util.irods :as irods]
            [data-info.util.validators :as duv]
            [ring.util.http-response :as http-response]))


;; id specific

(defn id-entry
  [url-id user]
  (try
    (irods/with-jargon-exceptions [cm]
      (if-not (user/user-exists? cm user)
        (http-response/unprocessable-entity)
        (if-let [path (uuid/get-path cm url-id)]
          (if (perm/is-readable? cm user path)
            (http-response/ok)
            (http-response/forbidden))
          (http-response/not-found))))
    (catch IllegalArgumentException _
      (http-response/unprocessable-entity))))


;; file specific

(defn- get-file
  [irods user path istream-ref]
  (if (zero? @(rods/file-size irods user (cfg/irods-zone) path))
    (clean-return (:jargon irods) "")
    (proxy-input-stream-return (:jargon irods) @istream-ref)))

(defn- file-entry
  [irods path {:keys [user attachment]}]
  (let [filename    (str \" (file/basename path) \")
        istream-ref (delay (io/input-stream (ops/input-stream @(:jargon irods) path)))
        disposition (if attachment
                      (str "attachment; filename=" filename)
                      (str "filename=" filename))
        media-type  (irods/detect-media-type (:jargon irods) path istream-ref)]
    (assoc (http-response/ok (get-file irods user path istream-ref))
           :headers {"Content-Type"        media-type
                     "Content-Disposition" disposition})))


; folder specific

(defn- validate-limit
  [limit]
  (when (nil? limit)
    (throw+ {:error_code error/ERR_MISSING_QUERY_PARAMETER :parameters :limit})))


(defn- resolve-bad-names
  [bad-name-params]
  (cond
    (nil? bad-name-params)    #{}
    (string? bad-name-params) #{bad-name-params}
    :else                     (set bad-name-params)))


(defn- resolve-bad-paths
  [bad-path-params]
  (letfn [(fmt-path [path-param]
            (if (= "/" path-param)
              "/"
              (let [res (file/rm-last-slash
                          (if (= \/ (first path-param))
                            path-param
                            (str \/ path-param)))]
                (when-not (empty? res) res))))]
    (if (string? bad-path-params)
      #{(fmt-path bad-path-params)}
      (->> bad-path-params (map fmt-path) (remove nil?) set))))


(defn- resolve-entity-type
  [entity-type-param]
  (if-not entity-type-param
    :any
    entity-type-param))


(defn- resolve-info-types
  [info-type-params]
  (cond
    (nil? info-type-params)    []
    (string? info-type-params) [info-type-params]
    :else                      info-type-params))


(def ^:private database-column-from-sort-field
  {:datecreated  :create-ts
   :datemodified :modify-ts
   :name         :base-name
   :path         :full-path
   :size         :data-size})


(defn- resolve-sort-field
  [sort-field-param]
  (if sort-field-param
    (database-column-from-sort-field sort-field-param sort-field-param)
    :base-name))


(defn- resolve-sort-dir
  [sort-dir-param]
  (if-not sort-dir-param
    :asc
    (case sort-dir-param
      "ASC"  :asc
      "DESC" :desc
      :asc)))


(defn- fmt-entry
  [id date-created date-modified bad? info-type path name permission size]
  {:id           (str id)
   :dateCreated  date-created
   :dateModified date-modified
   :badName      bad?
   :infoType     info-type
   :name         name
   :path         path
   :permission   permission
   :size         size})


(defn- page-entry->map
  "Turns a entry in a paged listing result into a map containing file/directory information that can
   be consumed by the front-end."
  [mark-bad?
   {:keys [access_type_id base_name create_ts data_size full_path info_type modify_ts uuid]}]
  (let [created  (* (Integer/parseInt create_ts) 1000)
        modified (* (Integer/parseInt modify_ts) 1000)
        bad?     (mark-bad? full_path)
        perm     (perm/fmt-perm access_type_id)]
    (fmt-entry uuid created modified bad? info_type full_path base_name perm data_size)))


(defn- page->map
  "Transforms an entire page of results for a paged listing in a map that can be returned to the
   client."
  [mark-bad? page]
  (let [entry-types (group-by :type page)
        do          (get entry-types "dataobject")
        collections (get entry-types "collection")
        xformer     (partial page-entry->map mark-bad?)]
    {:files   (mapv xformer do)
     :folders (mapv xformer collections)}))


(defn- is-bad?
  "Returns true if the map is okay to include in a directory listing."
  [bad-indicator path]
  (let [basename (file/basename path)]
    (or (contains? (:paths bad-indicator) path)
        (contains? (:names bad-indicator) basename)
        (not (duv/good-string? (:chars bad-indicator) basename)))))


(defn- paged-dir-listing
  "Provides paged directory listing as an alternative to (list-dir). Always contains files."
  [irods user path entity-type bad-indicator sfield sord offset limit info-types]
  (let [zone         (cfg/irods-zone)
        page         (rods/folder-listing irods user zone path
                                          :entity-type entity-type
                                          :info-types info-types
                                          :sort-column sfield
                                          :sort-direction sord
                                          :limit limit
                                          :offset offset)
        total        (delay (force page) @(rods/items-in-folder irods user zone path :entity-type entity-type :info-types info-types))
        id           (rods/uuid irods user zone path)
        bad?         (is-bad? bad-indicator path)
        perm         (rods/permission irods user zone path)
        date-created (rods/date-created irods user zone path)
        mod-date     (rods/date-modified irods user zone path)
        name         (fs/base-name path)]
    (clean-return (:jargon irods) ;; ensure that the with-jargon is closed out
      (merge (fmt-entry @id @date-created @mod-date bad? nil path name @perm 0)
             (page->map (partial is-bad? bad-indicator) @page)
             {:total    @total
              :totalBad 0}))))


(defn- folder-entry
  [irods path {:keys [user
                   entity-type
                   bad-chars
                   bad-name
                   bad-path
                   sort-field
                   sort-dir
                   offset
                   limit
                   info-type]}]
  (validate-limit limit)
  (let [badies {:chars (set bad-chars)
                :names (resolve-bad-names bad-name)
                :paths (resolve-bad-paths bad-path)}
        entity-type (resolve-entity-type entity-type)
        info-type   (resolve-info-types info-type)
        sort-field  (resolve-sort-field sort-field)
        sort-dir    (resolve-sort-dir sort-dir)
        offset      (if-not (nil? offset) offset 0)]
    (http-response/ok
      (paged-dir-listing
         irods user path entity-type badies sort-field sort-dir offset limit info-type))))


(defn- get-path-attrs
  [irods zone path-in-zone user]
  (let [path (file/rm-last-slash (irods/abs-path zone path-in-zone))
        object-type (rods/object-type irods user zone path)]
    (jv/validate-path-lengths path)
    (if (= @object-type :none)
      (throw+ {:error_code error/ERR_DOES_NOT_EXIST
               :path path})
      {:path           path
       :is-dir?        (= @object-type :dir)})))


(defn dispatch-path-to-resource
  [zone path-in-zone {:keys [user] :as params}]
  (irods/with-irods-exceptions {:jargon-opts {:client-user user :auto-close false}} irods
    (let [{:keys [path is-dir?]} (get-path-attrs irods zone path-in-zone user)]
      (if is-dir?
        (folder-entry irods path params)
        (file-entry irods path params)))))
