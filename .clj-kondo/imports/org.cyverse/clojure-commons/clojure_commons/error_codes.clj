(ns clojure-commons.error-codes)

(defmacro deferr
  [sym]
  `(def ~sym (name (quote ~sym))))
