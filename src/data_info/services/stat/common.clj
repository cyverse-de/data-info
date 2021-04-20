(ns data-info.services.stat.common
  (:require [clojure.set :as cset]
            [clojure.string :as string]
            [common-swagger-api.schema.stats :refer [AvailableStatFields]]
            [data-info.util.paths :as paths]))

(defn is-dir?
  "Returns true for any stat map map that contains information about a directory."
  [stat-map]
  (= (:type stat-map) :dir))

(defn owns?
  "Returns true for any stat map containing information about an entity that the user owns."
  [stat-map]
  (= (:permission stat-map) :own))

(defn needs-key?
  "Determins whether or not a key is needed in a stat map. Note: the :permission key is needed by anything that uses
   `owns?` and the :type key is needed by anything that uses `is-dir?`. In all other cases only requested keys are
   needed."
  [requested-keys needed-key?]
  (or (contains? requested-keys needed-key?)
      (case needed-key?
        :permission (contains? requested-keys :share-count)
        :type       (not (empty? (cset/intersection #{:infoType :content-type :file-count :dir-count} requested-keys)))
        false)))

(defn needs-any-key?
  "Determins whether or not any key in a set is needed in a stat map. Note: the :permission key is needed by anything
   that uses `owns?` and the :type key is needed by anything that uses `is-dir?`. In all other cases only requested
   keys are needed."
  [included-keys & needed-keys]
  (some #(needs-key? included-keys %) needed-keys))

(defmacro assoc-if-selected
  "Adds keys and values to a map. If the key is in the set of requested keys then the value
   is associated with the map. Otherwise, nil is associated with the map and the expression
   used to calculate the value is not executed."
  [m requested-keys & kvs]
  (let [format-kv (fn [[k v]] [k `(when (needs-key? ~requested-keys ~k) ~v)])]
    (concat `(assoc ~m) (mapcat format-kv (partition 2 kvs)))))

(defn merge-label
  "Merges an entity label into a stat map"
  [stat-map user path included-keys]
  (if (needs-key? included-keys :label)
    (assoc stat-map :label (paths/path->label user path))
    stat-map))

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
