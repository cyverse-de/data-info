(ns data-info.routes.schemas.common
  (:require [common-swagger-api.schema.data :as data-schema]))

(defn get-error-code-block
  [& error-codes]
  (str "

#### Error Codes:
    " (clojure.string/join "\n    " error-codes)))

(def DataIdPathParam data-schema/DataIdPathParam)

(def Paths data-schema/Paths)

(def PermissionEnum data-schema/PermissionEnum)
