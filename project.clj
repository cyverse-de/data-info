(use '[clojure.java.shell :only (sh)])
(require '[clojure.string :as string])

(defn git-ref
  []
  (or (System/getenv "GIT_COMMIT")
      (string/trim (:out (sh "git" "rev-parse" "HEAD")))
      ""))

(defproject org.cyverse/data-info "3.0.2-SNAPSHOT"
  :description "provides an HTTP API for interacting with iRODS"
  :url "https://github.com/cyverse-de/data-info"
  :license {:name "BSD"
            :url "http://iplantcollaborative.org/sites/default/files/iPLANT-LICENSE.txt"}
  :manifest {"Git-Ref" ~(git-ref)}
  :uberjar-name "data-info-standalone.jar"
  :dependencies [[cheshire "6.0.0"]
                 [com.cemerick/url "0.1.1" :exclusions [com.cemerick/clojurescript.test]]
                 [com.fasterxml.jackson.core/jackson-core "2.18.3"]
                 [com.novemberain/langohr "5.6.0"]
                 [javax.servlet/servlet-api "2.5"]
                 [me.raynes/fs "1.4.6"]
                 [metosin/compojure-api "1.1.14" :exclusions [ring/ring-codec]]
                 [net.sf.opencsv/opencsv "2.3"]
                 [org.apache.tika/tika-core "3.2.2" :exclusions [org.slf4j/slf4j-api]]
                 [org.clojure/clojure "1.12.1"]
                 [org.cyverse/async-tasks-client "0.0.5"]
                 [org.cyverse/clj-icat-direct "2.9.7"
                   :exclusions [[org.slf4j/slf4j-api]
                                [org.slf4j/slf4j-log4j12]
                                [log4j]]]
                 [org.cyverse/clj-irods "0.4.0"]
                 [org.cyverse/clj-jargon "3.1.4"
                   :exclusions [[org.slf4j/slf4j-api]
                                [org.slf4j/slf4j-log4j12]
                                [com.fasterxml.jackson.dataformat/jackson-dataformat-cbor]
                                [com.fasterxml.jackson.dataformat/jackson-dataformat-smile]
                                [com.fasterxml.jackson.core/jackson-annotations]
                                [com.fasterxml.jackson.core/jackson-databind]
                                [com.fasterxml.jackson.core/jackson-core]
                                [log4j]]]
                 [org.cyverse/clojure-commons "3.0.11"]
                 [org.cyverse/common-cfg "2.8.3"]
                 [org.cyverse/common-cli "2.8.2"]
                 [org.cyverse/common-swagger-api "3.4.12"]
                 [org.cyverse/dire "0.5.6"]
                 [org.cyverse/heuristomancer "2.8.7"]
                 [org.cyverse/kameleon "3.0.10"]
                 [org.cyverse/metadata-client "3.2.0"]
                 [org.cyverse/metadata-files "2.1.1"]
                 [org.cyverse/oai-ore "1.0.4"]
                 [org.cyverse/service-logging "2.8.4"]
                 [org.slf4j/slf4j-api "2.0.17"]
                 [ring/ring-codec "1.3.0"]
                 [ring/ring-jetty-adapter "1.14.2" :exclusions [org.slf4j/slf4j-api]]
                 [slingshot "0.12.2"]]
  :eastwood {:exclude-namespaces [data-info.routes.schemas.tickets
                                  data-info.routes.schemas.stats
                                  data-info.routes.schemas.sharing
                                  data-info.routes.schemas.trash
                                  :test-paths]
             :linters [:wrong-arity :wrong-ns-form :wrong-pre-post :wrong-tag :misplaced-docstrings]}
  :plugins [[jonase/eastwood "1.4.3"]
            [lein-ancient "1.0.0-RC3"]
            [test2junit "1.4.4"]]
  :profiles {:dev     {:plugins        [[lein-ring "0.12.5"]]
                       :resource-paths ["conf/test"]}
             :repl    {:source-paths ["repl"]}
             :uberjar {:aot :all}}
  :main ^:skip-aot data-info.core
  :ring {:handler data-info.routes/app
         :init data-info.core/lein-ring-init
         :port 31360
         :auto-reload? false}
  :uberjar-exclusions [#".*[.]SF" #"LICENSE" #"NOTICE"]
  :jvm-opts ["-Dlogback.configurationFile=/etc/iplant/de/logging/data-info-logging.xml"])
