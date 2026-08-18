(ns data-info.services.exists-test
  (:require [clojure.test :refer [deftest testing are is]]
            [data-info.services.exists :as exists]))

(def ^:private ancestors-of #'exists/ancestors-of)

(deftest ancestors-of-walks-from-the-path-up-to-the-root
  (testing "the path itself comes first, since a path that already exists is its own deepest ancestor"
    (are [path expected] (= expected (ancestors-of path))
      "/iplant/home/me/a/b" ["/iplant/home/me/a/b" "/iplant/home/me/a" "/iplant/home/me" "/iplant/home" "/iplant" "/"]
      "/iplant/home"        ["/iplant/home" "/iplant" "/"]
      "/iplant"             ["/iplant" "/"]
      "/"                   ["/"]))
  (testing "the walk terminates rather than repeating the root forever"
    (is (= 6 (count (ancestors-of "/iplant/home/me/a/b"))))))
