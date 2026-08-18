(ns data-info.util.listings-test
  (:require [clojure.test :refer [deftest testing are]]
            [data-info.util.listings :as listings]))

(deftest resolve-sort-field-maps-onto-icat-columns
  (testing "each supported sort field maps onto the column the catalog sorts by"
    (are [param expected] (= expected (listings/resolve-sort-field param))
      :datecreated  :create-ts
      :datemodified :modify-ts
      :name         :base-name
      :path         :full-path
      :size         :data-size))
  (testing "an absent sort field sorts by name, and anything else is passed through untouched"
    (are [param expected] (= expected (listings/resolve-sort-field param))
      nil       :base-name
      :type     :type
      :base-name :base-name)))

(deftest resolve-sort-dir-defaults-to-ascending
  (are [param expected] (= expected (listings/resolve-sort-dir param))
    "ASC"       :asc
    "DESC"      :desc
    nil         :asc
    "asc"       :asc
    "sideways"  :asc))

(deftest resolve-info-types-normalizes-to-a-collection
  (are [param expected] (= expected (listings/resolve-info-types param))
    nil             []
    "csv"           ["csv"]
    ["csv"]         ["csv"]
    ["csv" "tsv"]   ["csv" "tsv"]))
