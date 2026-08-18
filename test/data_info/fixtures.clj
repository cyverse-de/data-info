(ns data-info.fixtures
  (:require [data-info.util.config :as config]))

(defn with-default-properties
  "Loads the test configuration. The config namespace is reloaded first, because it holds the
   settings in state that another suite in the same run may already have populated."
  [f]
  (require 'data-info.util.config :reload)
  (config/load-config-from-file "conf/test/mostly-defaults.properties")
  (f))
