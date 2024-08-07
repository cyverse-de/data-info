(ns data-info.core
  (:gen-class)
  (:require [clojure.java.io :as io]
            [clojure.tools.logging :as log]
            [data-info.routes :as routes]
            [data-info.util.config :as config]
            [data-info.amqp :as amqp]
            [me.raynes.fs :as fs]
            [common-cli.core :as ccli]
            [service-logging.thread-context :as tc]
            [data-info.services.icat :as icat]))


(defn- iplant-conf-dir-file
  [filename]
  (when-let [conf-dir (System/getenv "IPLANT_CONF_DIR")]
    (let [f (io/file conf-dir filename)]
      (when (.isFile f) (.getPath f)))))


(defn- cwd-file
  [filename]
  (let [f (io/file filename)]
    (when (.isFile f) (.getPath f))))


(defn- classpath-file
  [filename]
  (-> (Thread/currentThread)
      (.getContextClassLoader)
      (.findResource filename)
      (.toURI)
      io/file))


(defn- no-configuration-found
  [filename]
  (throw (RuntimeException. (str "configuration file " filename " not found"))))


(defn- find-configuration-file
  []
  (let [conf-file "data-info.properties"]
    (or (iplant-conf-dir-file conf-file)
        (cwd-file conf-file)
        (classpath-file conf-file)
        (no-configuration-found conf-file))))


(defn- load-configuration-from-file
  "Loads the configuration properties from a file."
  ([]
   (load-configuration-from-file (find-configuration-file)))

  ([path]
    (config/load-config-from-file path)))


(defn lein-ring-init
  []
  (load-configuration-from-file)
  (icat/configure-icat))


(defn repl-init
  []
  (load-configuration-from-file)
  (icat/configure-icat))


(defn- cli-options
  []
  [["-c" "--config PATH" "Path to the config file"
    :default "/etc/iplant/de/data-info.properties"]
   ["-v" "--version" "Print out the version number."]
   ["-h" "--help"]])

(defn run-jetty
  []
  (require 'ring.adapter.jetty)
  (log/warn "Started listening on" (config/listen-port))
  ((eval 'ring.adapter.jetty/run-jetty) routes/app {:port (config/listen-port)}))

(defn -main
  [& args]
  (tc/with-logging-context config/svc-info
    (let [{:keys [options arguments errors summary]} (ccli/handle-args config/svc-info
                                                                       args
                                                                       cli-options)]
      (when-not (fs/exists? (:config options))
        (ccli/exit 1 (str "The config file does not exist.")))
      (when-not (fs/readable? (:config options))
        (ccli/exit 1 "The config file is not readable."))
      (load-configuration-from-file (:config options))
      (icat/configure-icat)
      (run-jetty))))
