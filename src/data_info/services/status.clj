(ns data-info.services.status
  (:require [clojure.tools.logging :as log]
            [clj-jargon.init :as init]
            [clj-jargon.item-info :as item]
            [clojure-commons.error-codes :as ce]
            [otel.otel :as otel]
            [data-info.util.config :as cfg]))


(defn ^Boolean irods-running?
  "Determines whether or not iRODS is running."
  []
  (otel/with-span [s ["irods-running?"]]
    (try
      (init/with-jargon (cfg/jargon-cfg) [cm]
        (item/exists? cm (:home cm)))
      (catch Exception e
        (log/error "Error performing iRODS status check:")
        (log/error (ce/format-exception e))
        false))))
