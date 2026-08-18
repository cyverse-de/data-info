(ns data-info.util.listings
  "Query parameter resolution shared by the paged listing endpoints.")

(def ^:private database-column-from-sort-field
  {:datecreated  :create-ts
   :datemodified :modify-ts
   :name         :base-name
   :path         :full-path
   :size         :data-size})

(defn resolve-sort-field
  "Maps a sort-field query parameter onto the ICAT column it sorts by, defaulting to the entity name."
  [sort-field-param]
  (if sort-field-param
    (database-column-from-sort-field sort-field-param sort-field-param)
    :base-name))

(defn resolve-sort-dir
  "Maps a sort-dir query parameter onto a sort direction, defaulting to ascending."
  [sort-dir-param]
  (if-not sort-dir-param
    :asc
    (case sort-dir-param
      "ASC"  :asc
      "DESC" :desc
      :asc)))

(defn resolve-info-types
  "Normalizes the info-type query parameter, which may be absent, a single value, or a list."
  [info-type-params]
  (cond
    (nil? info-type-params)    []
    (string? info-type-params) [info-type-params]
    :else                      info-type-params))
