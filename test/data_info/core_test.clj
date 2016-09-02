(ns data-info.core-test
  (:use clojure.test
        data-info.core)
  (:require [data-info.util.config :as config]))

(defn with-default-properties [f]
  (require 'data-info.util.config :reload)
  (config/load-config-from-file "conf/test/mostly-defaults.properties")
  (f))

(use-fixtures :once with-default-properties)

(deftest test-config-defaults.
  (testing "default configuration settings"
    (is (= (config/listen-port) 60000))
    (is (= (config/perms-filter) ["rods", "rodsadmin"]))
    (is (= (config/community-data) "/iplant/home/shared"))
    (is (= (config/copy-attribute) "ipc-de-copy-from"))
    (is (= (config/bad-chars) "\u0060\u0027\u000A\u0009"))
    (is (= (config/max-paths-in-request)  1000))
    (is (= (config/anon-user) "anonymous"))
    (is (= (config/anon-files-base-url) "https://de.example.org/anon-files/"))
    (is (= (config/metadata-base-url) "http://metadata:60000"))
    (is (= (config/tree-urls-base-url) "http://tree-urls:60000"))
    (is (= (config/kifshare-download-template) "{{url}}/d/{{ticket-id}}/{{filename}}"))
    (is (= (config/kifshare-external-url) "http://de.example.org/dl"))
    (is (= (config/irods-home) "/iplant/home"))
    (is (= (config/irods-user) "rods"))
    (is (= (config/irods-password) "notprod"))
    (is (= (config/irods-host) "irods"))
    (is (= (config/irods-port) "1247"))
    (is (= (config/irods-zone) "iplant"))
    (is (= (config/irods-resc) ""))
    (is (= (config/irods-max-retries) 10))
    (is (= (config/irods-retry-sleep) 1000))
    (is (= (config/irods-use-trash) true))
    (is (= (config/irods-admins) ["rods", "rodsadmin"]))
    (is (= (config/icat-host) "irods"))
    (is (= (config/icat-user) "rods"))
    (is (= (config/icat-password) "notprod"))
    (is (= (config/icat-db) "ICAT"))
    (is (= (config/type-detect-type-attribute) "ipc-filetype"))))
