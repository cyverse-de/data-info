(ns data-info.services.stat-test
  (:require [clojure.test :refer [deftest are]]
            [data-info.services.stat :as stat]))

(def ^:private listing-row-type #'stat/listing-row-type)

(deftest listing-row-type-maps-catalog-types-onto-stat-types
  (are [row expected] (= expected (listing-row-type row))
    {:type "collection"} :dir
    {:type "dataobject"} :file))
