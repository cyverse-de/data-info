(ns data-info.routes-test
  (:require [cheshire.core :as json]
            [clojure.test :refer [deftest testing is use-fixtures]]
            [data-info.routes :as routes]
            [data-info.util.config :as config]))

(defn- with-default-properties [f]
  (require 'data-info.util.config :reload)
  (config/load-config-from-file "conf/test/mostly-defaults.properties")
  (f))

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
