(ns data-info.routes.schemas.common
  (:require [common-swagger-api.schema.data :as data-schema]
            [schema.core :as s]))

(defn ->required-param
  "Removes an optional param from the given schema and re-adds it as a required param. The inverse of
   `common-swagger-api.schema/->optional-param`."
  [schema param]
  (-> schema
      (assoc param (schema (s/optional-key param)))
      (dissoc (s/optional-key param))))

(defn get-error-code-block
  [& error-codes]
  (str "

#### Error Codes:
    " (clojure.string/join "\n    " error-codes)))

(def DataIdPathParam data-schema/DataIdPathParam)

(def Paths data-schema/Paths)

(def PermissionEnum data-schema/PermissionEnum)
