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
  ;; Fail the build on a new dependency conflict rather than printing a
  ;; warning nobody reads.
  :pedantic? :abort
  ;; Records versions Leiningen already resolves, read off the resolved
  ;; classpath rather than copied from lein's "Consider using these
  ;; :managed-dependencies" hint -- that hint names the version that LOST the
  ;; conflict, so pasting it would be a silent upgrade.
  ;;
  ;; jackson-annotations and jackson-databind are pinned to keep the family
  ;; together: cheshire 6.2.0 moves core/cbor/smile to 2.21.1, and on main the
  ;; family was coherent at 2.18.3. A jackson mismatch surfaces as a runtime
  ;; NoSuchMethodError rather than a resolution failure, and :pedantic? cannot
  ;; see it because each artifact is individually unambiguous. This repo already
  ;; excludes every jackson artifact from clj-jargon, so it is free to align
  ;; upward rather than being held at jargon's 2.14.1.
  :managed-dependencies [[clj-http "3.13.0"]
                         [com.fasterxml.jackson.core/jackson-annotations "2.21"]
                         [com.fasterxml.jackson.core/jackson-databind "2.21.1"]
                         [com.google.guava/guava "16.0.1"]
                         [commons-codec "1.16.1"]
                         [org.apache.commons/commons-compress "1.8"]
                         [prismatic/schema "1.1.12"]]
  :dependencies [[cheshire "6.2.0"]
                 [com.cemerick/url "0.1.1" :exclusions [com.cemerick/clojurescript.test]]
                 [com.fasterxml.jackson.core/jackson-core "2.21.1"]
                 [com.novemberain/langohr "5.6.0"]
                 [javax.servlet/servlet-api "2.5"]
                 [me.raynes/fs "1.4.6"]
                 [metosin/compojure-api "1.1.14" :exclusions [ring/ring-codec]]
                 [net.sf.opencsv/opencsv "2.3"]
                 [org.apache.tika/tika-core "3.3.2" :exclusions [org.slf4j/slf4j-api]]
                 [org.clojure/clojure "1.12.5"]
                 [org.cyverse/async-tasks-client "0.0.6"]
                 [org.cyverse/clj-icat-direct "2.9.8"
                   :exclusions [[org.slf4j/slf4j-api]
                                [org.slf4j/slf4j-log4j12]
                                [log4j]]]
                 [org.cyverse/clj-irods "0.4.2"]
                 [org.cyverse/clj-jargon "3.1.6"
                   :exclusions [[org.slf4j/slf4j-api]
                                [org.slf4j/slf4j-log4j12]
                                [com.fasterxml.jackson.dataformat/jackson-dataformat-cbor]
                                [com.fasterxml.jackson.dataformat/jackson-dataformat-smile]
                                [com.fasterxml.jackson.core/jackson-annotations]
                                [com.fasterxml.jackson.core/jackson-databind]
                                [com.fasterxml.jackson.core/jackson-core]
                                [log4j]]]
                 [org.cyverse/clojure-commons "3.0.13"]
                 [org.cyverse/common-cfg "2.8.4"]
                 [org.cyverse/common-cli "2.8.3"]
                 [org.cyverse/common-swagger-api "3.4.23"]
                 [org.cyverse/dire "0.5.6"]
                 [org.cyverse/heuristomancer "2.8.8"]
                 [org.cyverse/kameleon "3.0.11"]
                 [org.cyverse/metadata-client "3.2.2"]
                 [org.cyverse/metadata-files "2.1.2"]
                 [org.cyverse/oai-ore "1.0.4"]
                 [org.cyverse/service-logging "2.8.6"]
                 [org.slf4j/slf4j-api "2.0.18"]
                 [ring/ring-codec "1.3.0"]
                 [ring/ring-jetty-adapter "1.15.5" :exclusions [org.slf4j/slf4j-api]]
                 [slingshot "0.12.2"]]
  :eastwood {:exclude-namespaces [data-info.routes.schemas.tickets
                                  data-info.routes.schemas.stats
                                  data-info.routes.schemas.sharing
                                  data-info.routes.schemas.trash
                                  :test-paths]
             :linters [:wrong-arity :wrong-ns-form :wrong-pre-post :wrong-tag :misplaced-docstrings]}
  :plugins [[jonase/eastwood "1.4.3"]
            [lein-ancient "1.0.0"]
            [test2junit "1.4.4"]]
  :profiles {:dev     {:plugins        [[lein-ring "0.12.6"]]
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
