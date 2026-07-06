(ns data-info.util.jetty
  "Jetty customization: per-endpoint connection idle timeouts.

   The ring-jetty adapter exposes only a single connector-level idle timeout, so a longer
   grace period for slow uploads would otherwise apply to every endpoint. Installing an
   HttpConfiguration customizer lets us keep a short default idle timeout while raising it
   only for the (long-lived, streaming) upload endpoints."
  (:import [org.eclipse.jetty.server Server Connector HttpConnectionFactory HttpConfiguration$Customizer]))

(defn- idle-timeout-customizer
  "A per-request Jetty customizer. Sets the connection's idle timeout to upload-idle-ms for
   requests whose path satisfies upload-path?, and to default-idle-ms otherwise (so the value
   is correct even when a connection is reused across requests)."
  [upload-path? ^long default-idle-ms ^long upload-idle-ms]
  (reify HttpConfiguration$Customizer
    (customize [_ request _response-headers]
      (let [endpoint (.. request getConnectionMetaData getConnection getEndPoint)
            path     (.. request getHttpURI getPath)]
        (.setIdleTimeout endpoint (if (upload-path? path) upload-idle-ms default-idle-ms)))
      request)))

(defn idle-timeout-configurator
  "Returns a ring-jetty :configurator fn that installs idle-timeout-customizer on every HTTP
   connection factory of the server."
  [upload-path? default-idle-ms upload-idle-ms]
  (fn [^Server server]
    (let [customizer (idle-timeout-customizer upload-path? (long default-idle-ms) (long upload-idle-ms))]
      (doseq [^Connector connector (.getConnectors server)
              factory              (.getConnectionFactories connector)
              :when                (instance? HttpConnectionFactory factory)]
        (.addCustomizer (.getHttpConfiguration ^HttpConnectionFactory factory) customizer)))))
