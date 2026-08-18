(ns data-info.services.stat-test
  (:require [clojure.test :refer [deftest testing are]]
            [data-info.services.stat :as stat]))

(def ^:private listing-row-type #'stat/listing-row-type)
(def ^:private listing-row->stat #'stat/listing-row->stat)

(deftest listing-row-type-maps-catalog-types-onto-stat-types
  (are [row expected] (= expected (listing-row-type row))
    {:type "collection"} :dir
    {:type "dataobject"} :file))

(deftest listing-row-becomes-the-stat-the-catalog-already-answered
  (testing "timestamps are seconds in the catalog and milliseconds in a stat"
    (are [row expected] (= expected (listing-row->stat row))
      {:type "dataobject" :full_path "/iplant/home/me/a.txt" :data_size 12
       :create_ts "1700000000" :modify_ts "1700000060" :data_checksum "abc123"}
      {:date-created  1700000000000
       :date-modified 1700000060000
       :path          "/iplant/home/me/a.txt"
       :type          :file
       :file-size     12
       :md5           "abc123"}))
  (testing "columns the catalog leaves empty are left out rather than reported as nil"
    (are [row expected] (= expected (listing-row->stat row))
      {:type "collection" :full_path "/iplant/home/me" :data_size 0
       :create_ts "1700000000" :modify_ts "1700000060" :data_checksum nil}
      {:date-created  1700000000000
       :date-modified 1700000060000
       :path          "/iplant/home/me"
       :type          :dir}

      {:type "dataobject" :full_path "/iplant/home/me/b.txt" :data_size 0
       :create_ts "1700000000" :modify_ts "1700000060" :data_checksum nil}
      {:date-created  1700000000000
       :date-modified 1700000060000
       :path          "/iplant/home/me/b.txt"
       :type          :file
       :file-size     0})))
