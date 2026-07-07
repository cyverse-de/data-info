(ns clj-kondo.exports.org.cyverse.clojure-commons.clojure-commons.file-utils)

(defn random-str
  []
  (let [allowed-chars "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"]
    (apply str (map (partial nth allowed-chars) (take 16 (repeatedly #(int (* (rand) (count allowed-chars)))))))))

(defmacro with-temp-dir
  [sym prefix _err-fn & body]
  `(let [~sym (str ~prefix (random-str))]
     ~@body))

(defmacro with-temp-dir-in
  [sym parent prefix _err-fn & body]
  `(let [~sym (str ~parent "/" ~prefix (random-str))]
     ~@body))
