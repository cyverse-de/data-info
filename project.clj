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
  :dependencies [[org.clojure/clojure "1.11.4"]
                 [cheshire "5.13.0"
                   :exclusions [[com.fasterxml.jackson.dataformat/jackson-dataformat-cbor]
                                [com.fasterxml.jackson.dataformat/jackson-dataformat-smile]
                                [com.fasterxml.jackson.core/jackson-annotations]
                                [com.fasterxml.jackson.core/jackson-databind]
                                [com.fasterxml.jackson.core/jackson-core]]]
                 [com.cemerick/url "0.1.1" :exclusions [com.cemerick/clojurescript.test]]
                 [org.cyverse/dire "0.5.6"]
                 [me.raynes/fs "1.4.6"]
                 [org.apache.tika/tika-core "2.9.2" :exclusions [org.slf4j/slf4j-api]]
                 [net.sf.opencsv/opencsv "2.3"]
                 [org.cyverse/clj-irods "0.3.6"]
                 [org.cyverse/clj-icat-direct "2.9.6"
                   :exclusions [[org.slf4j/slf4j-api]
                                [org.slf4j/slf4j-log4j12]
                                [log4j]]]
                 [org.cyverse/clj-jargon "3.1.2"
                   :exclusions [[org.slf4j/slf4j-api]
                                [org.slf4j/slf4j-log4j12]
                                [log4j]]]
                 [javax.servlet/servlet-api "2.5"]
                 [metosin/compojure-api "1.1.14"]
                 [ring/ring-jetty-adapter "1.12.2" :exclusions [org.slf4j/slf4j-api]]
                 [org.cyverse/clojure-commons "3.0.9"]
                 [org.cyverse/common-cli "2.8.2"]
                 [org.cyverse/common-cfg "2.8.3"]
                 [org.cyverse/common-swagger-api "3.4.5"]
                 [org.cyverse/heuristomancer "2.8.7"]
                 [org.cyverse/kameleon "3.0.10"]
                 [org.cyverse/metadata-client "3.1.2"]
                 [org.cyverse/async-tasks-client "0.0.5"]
                 [org.cyverse/metadata-files "2.1.1"]
                 [org.cyverse/oai-ore "1.0.4"]
                 [org.cyverse/service-logging "2.8.4"]
                 [com.novemberain/langohr "5.4.0"]
                 [slingshot "0.12.2"]]
  :eastwood {:exclude-namespaces [data-info.routes.schemas.tickets
                                  data-info.routes.schemas.stats
                                  data-info.routes.schemas.sharing
                                  data-info.routes.schemas.trash
                                  :test-paths]
             :linters [:wrong-arity :wrong-ns-form :wrong-pre-post :wrong-tag :misplaced-docstrings]}
  :plugins [[jonase/eastwood "1.4.3"]
            [lein-ancient "0.7.0"]
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
