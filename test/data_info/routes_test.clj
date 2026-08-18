(ns data-info.routes-test
  (:require [cheshire.core :as json]
            [clojure.test :refer [are deftest testing is use-fixtures]]
            [data-info.fixtures :refer [with-default-properties]]
            [data-info.routes :as routes])
  (:import [java.io ByteArrayInputStream]))

(use-fixtures :once with-default-properties)

(def ^:private swagger-request
  {:request-method :get
   :uri            "/swagger.json"
   :headers        {}
   :scheme         :http
   :server-name    "localhost"
   :server-port    60000})

(defn- swagger-paths []
  (let [body (:body (routes/app swagger-request))]
    (-> (if (string? body) body (slurp body))
        (json/parse-string true)
        :paths
        keys
        set)))

(deftest endpoints-terrain-proxies-to-are-registered
  (testing "the routes that let terrain stop talking to iRODS directly are all published"
    (let [paths (swagger-paths)]
      (doseq [path ["/sharer" "/unsharer" "/creatability-marker" "/stat-lister"]]
        (is (contains? paths (keyword path)) (str path " is missing from the API"))))))

(defn- stat-listing-status [query-string]
  (:status (routes/app {:request-method :post
                        :uri            "/stat-lister"
                        :query-string   query-string
                        :headers        {"content-type" "application/json"}
                        :body           (ByteArrayInputStream. (.getBytes (json/encode {:ids []})))
                        :scheme         :http
                        :server-name    "localhost"
                        :server-port    60000})))

(deftest stat-listing-rejects-paging-the-catalog-cannot-run
  (testing "a limit or offset the catalog would choke on is rejected before it gets there"
    (are [query-string] (= 400 (stat-listing-status query-string))
      "user=me&limit=-1&offset=0"
      "user=me&limit=0&offset=0"
      "user=me&limit=10&offset=-3"
      "user=me&offset=0"
      "user=me&limit=10")))
