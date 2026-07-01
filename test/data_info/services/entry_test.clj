(ns data-info.services.entry-test
  (:require [clojure.test :refer :all]
            [clojure.string :as string]
            [data-info.services.entry :as entry])
  (:import [java.net URLDecoder]))

(def ^:private content-disposition #'entry/content-disposition)

(defn- ascii-only? [s]
  (every? #(< (int %) 128) s))

(defn- filename*-value [header]
  (second (re-find #"filename\*=UTF-8''(\S+)" header)))

(deftest content-disposition-preserves-non-ascii
  ;; ä/ö/ü are <= U+00FF and survived the old code; ł/ō are > U+00FF and were
  ;; stripped by the ISO-8859-1 header transport. Both must round-trip now.
  (let [filename "test-ä-ö-ü-ł-ō.txt"
        header   (content-disposition true filename)]
    (testing "the header is pure ASCII, so it survives the ISO-8859-1 header transport"
      (is (ascii-only? header)))
    (testing "filename* round-trips back to the original UTF-8 name"
      (is (= filename (URLDecoder/decode (filename*-value header) "UTF-8"))))
    (testing "no raw non-ASCII characters leak into the header"
      (is (not (string/includes? header "ł"))))))

(deftest content-disposition-fallback-is-safe
  (testing "characters that would break a quoted filename= are replaced in the fallback"
    (let [header (content-disposition true "a\"b\\c.txt")]
      (is (string/includes? header "filename=\"a_b_c.txt\""))
      (testing "but filename* preserves them exactly"
        (is (= "a\"b\\c.txt" (URLDecoder/decode (filename*-value header) "UTF-8")))))))

(deftest content-disposition-disposition-type
  (are [attachment expected] (string/starts-with? (content-disposition attachment "f.txt") expected)
    true  "attachment;"
    false "inline;"))
