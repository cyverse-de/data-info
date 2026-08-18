(ns data-info.services.sharing-test
  (:require [clojure.test :refer [deftest testing are]]
            [data-info.services.sharing :as sharing]))

(def ^:private outcome->item #'sharing/outcome->item)

(deftest outcome-reports-a-skip-as-a-success-with-a-reason
  (testing "a skip is a success, because the end state is what the caller asked for"
    (are [outcome expected] (= expected (outcome->item {:path "/iplant/home/me/a"} outcome))
      {:user "u" :path "/iplant/home/me/a"}
      {:path "/iplant/home/me/a" :success true}

      {:user "u" :path "/iplant/home/me/a" :reason :already-shared :skipped true}
      {:path "/iplant/home/me/a" :success true :reason "already-shared"}

      {:user "u" :path "/iplant/home/me/a" :reason :share-with-self :skipped true}
      {:path "/iplant/home/me/a" :success true :reason "share-with-self"}

      {:user "u" :path "/iplant/home/me/a" :reason :share-from-trash :skipped true}
      {:path "/iplant/home/me/a" :success true :reason "share-from-trash"}))
  (testing "the request item is carried through, so a share keeps the permission it asked for"
    (are [item outcome expected] (= expected (outcome->item item outcome))
      {:path "/p" :permission :read} {:user "u" :path "/p"}
      {:path "/p" :permission :read :success true}

      {:path "/p" :permission :write} {:user "u" :path "/p" :reason :not-shared :skipped true}
      {:path "/p" :permission :write :success true :reason "not-shared"})))
