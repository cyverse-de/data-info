(ns data-info.services.sharing-test
  (:require [clojure.test :refer [deftest testing are is]]
            [clojure-commons.error-codes :as error]
            [data-info.services.sharing :as sharing])
  (:import [java.io IOException]
           [org.irods.jargon.core.exception JargonException]))

(def ^:private outcome->item #'sharing/outcome->item)
(def ^:private per-path-failure #'sharing/per-path-failure)

(deftest a-path-fails-on-its-own-unless-irods-is-gone
  (testing "a rejection naming one path is reported against that path"
    (are [e expected] (= expected (per-path-failure e))
      {:error_code error/ERR_NOT_OWNER :path "/p"}
      {:error_code error/ERR_NOT_OWNER :path "/p"}

      {:error_code error/ERR_DOES_NOT_EXIST :path "/p"}
      {:error_code error/ERR_DOES_NOT_EXIST :path "/p"}))
  (testing "iRODS refusing one operation is reported against that path, not the whole request"
    (is (= {:error_code error/ERR_REQUEST_FAILED :reason "no permission"}
           (per-path-failure (JargonException. "no permission")))))
  (testing "an unreachable iRODS has to fail the request, since the next path will fare no better"
    (is (nil? (per-path-failure (JargonException. "gone" (IOException. "connection reset")))))))

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
