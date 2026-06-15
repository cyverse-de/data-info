(ns data-info.amqp
  (:require [clojure.tools.logging :as log]
            [data-info.util.config :as config]
            [service-logging.thread-context :as tc]
            [cheshire.core :as cheshire]
            [langohr.core :as rmq]
            [langohr.channel :as lch]
            [langohr.exchange :as le]
            [langohr.basic :as lb]))

;; Persistent AMQP connection state.  A single atom holds both the connection
;; and channel so they are always replaced as an atomic pair.
(defonce ^:private amqp-state (atom nil))

;; Monitor object used to serialize all publish and reconnect operations.
;; RabbitMQ channels are not thread-safe for concurrent use, and we must also
;; prevent multiple threads from racing through reconnect simultaneously.
(defonce ^:private publish-lock (Object.))

(defn- declare-exchange
  [channel]
  (le/topic channel
            (config/exchange-name)
            {:durable     (config/exchange-durable?)
             :auto-delete (config/exchange-auto-delete?)}))

(defn connect!
  "Opens a connection and channel to the AMQP broker, declares the exchange,
  and stores both in the module-level atom.  The old channel and connection
  (if any) are closed on a best-effort basis.

  The state swap and old-connection teardown are performed under `publish-lock`
  so that no publish can observe a partially-replaced state."
  []
  (let [uri  (config/amqp-uri)
        host (get (rmq/parse-uri uri) :host "unknown")]
    (log/info "[amqp/connect!] Connecting to AMQP broker:" host)
    (let [conn (rmq/connect {:uri uri})
          ch   (lch/open conn)]
      (declare-exchange ch)
      (locking publish-lock
        (let [old @amqp-state]
          (reset! amqp-state {:conn conn :channel ch})
          (when old
            (try (lch/close (:channel old)) (catch Exception _ nil))
            (try (rmq/close (:conn    old)) (catch Exception _ nil)))))
      (log/info "[amqp/connect!] Connected."))))

(defn disconnect!
  "Closes the channel and connection.  Intended for clean service shutdown."
  []
  (locking publish-lock
    (when-let [{:keys [conn channel]} @amqp-state]
      (log/info "[amqp/disconnect!] Closing AMQP connection.")
      (try (lch/close channel) (catch Exception _ nil))
      (try (rmq/close conn)    (catch Exception _ nil))
      (reset! amqp-state nil))))

(defn- do-publish
  [routing-key encoded-body time-now]
  (let [ch (:channel @amqp-state)]
    (when (nil? ch)
      (throw (IllegalStateException. "AMQP channel not available. Has connect! been called?")))
    (lb/publish ch
                (config/exchange-name)
                routing-key
                encoded-body
                {:content-type "application/json"
                 :timestamp    time-now})))

(defn publish-msg
  "Publishes a message to the AMQP exchange.  On failure the connection is
  re-established and the publish is retried once.  If the retry also fails
  the error is logged and the exception is suppressed.

  The entire publish-with-retry sequence is serialized via `publish-lock` to
  ensure the underlying channel is never used concurrently and to prevent
  multiple threads from stampeding through reconnect at the same time."
  [routing-key msg]
  (let [time-now     (new java.util.Date)
        encoded-body (cheshire/encode {:message      msg
                                       :timestamp_ms (.getTime time-now)})]
    (tc/with-logging-context
      {:amqp-routing-key routing-key
       :amqp-message msg}
      (log/info (format "Publishing AMQP message. routing-key=%s" routing-key)))
    (locking publish-lock
      (try
        (do-publish routing-key encoded-body time-now)
        (catch Exception e
          (log/warn e "[amqp/publish-msg] Publish failed; reconnecting and retrying.")
          (try
            (connect!)
            (do-publish routing-key encoded-body time-now)
            (catch Exception re
              (log/error re "[amqp/publish-msg] Retry failed. Message lost."
                         (cheshire/encode msg)))))))))
