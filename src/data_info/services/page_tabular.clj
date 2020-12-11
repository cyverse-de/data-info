(ns data-info.services.page-tabular
  (:use [clojure-commons.error-codes]
        [clojure-commons.validators]
        [clj-jargon.item-info :only [file-size]]
        [clj-jargon.paging :only [read-at-position]]
        [slingshot.slingshot :only [try+ throw+]])
  (:require [clojure.tools.logging :as log]
            [cemerick.url :as url]
            [clojure.string :as string]
            [clojure-commons.file-utils :as ft]
            [clj-irods.core :as rods]
            [clj-irods.validate :refer [validate]]
            [otel.otel :as otel]
            [dire.core :refer [with-pre-hook! with-post-hook!]]
            [data-info.services.uuids :as uuids]
            [data-info.util.config :as cfg]
            [data-info.util.logging :as dul]
            [data-info.util.irods :as irods]
            [data-info.util.validators :as validators])
  (:import [au.com.bytecode.opencsv CSVReader]
           [java.io InputStream]))

(def ^:private ^String line-ending "\n")

(defn read-csv-stream
  [^String separator ^InputStream stream]
  (otel/with-span [s ["read-csv-stream"]]
    (.readAll (CSVReader. stream (.charAt separator 0)))))

(defn- chunk-start
  [page chunk-size]
  (if (= page 0)
    0
    (dec chunk-size)))

(defn- seek-prev-line
  [chunk start-pos]
  (log/debug "seeking for a line-ending backwards starting at" start-pos)
  (loop [pos start-pos]
    (let [^Character start-str (nth chunk pos)]
      (if-not (or (= pos 0)
                  (= start-str (.charAt line-ending 0)))
        (recur (dec pos))
        (if (= pos 0)
            (do (log/debug "defaulting to assuming line ending before position 0") 0)
            (do (log/debug "found line ending at position" (inc pos)) (inc pos)))))))

(defn- end-pos
  [^String chunk page pages]
  (if (= page (dec pages))
    (.length chunk)
    (seek-prev-line chunk (dec (.length chunk)))))

(defn- trim-chunk
  [^String chunk chunk-size page pages]
  (otel/with-span [s ["trim-chunk"]]
    (log/debug "untrimmed chunk:" chunk)
    (let [lstart (seek-prev-line chunk (chunk-start page chunk-size))
          lend   (end-pos chunk page pages)]
      (.substring chunk lstart lend))))

(defn- fix-record
  [record]
  (into {}
    (map (fn [[k v]] (if (nil? v) [k ""] [k v]))
         (seq record))))

(defn- read-csv
  [^String csv-str ^String separator]
  (otel/with-span [s ["read-csv"]]
    (if-not (string/blank? csv-str)
      (let [ba  (java.io.ByteArrayInputStream. (.getBytes csv-str))
            isr (java.io.InputStreamReader. ba "UTF-8")]
        (map fix-record (mapv #(zipmap (mapv str (range (count %1))) %1)
                             (mapv vec (read-csv-stream separator isr)))))
      [{}])))

(defn- num-pages
  [chunk-size file-size]
  (int (Math/ceil (double (/ file-size chunk-size)))))

(defn- start-pos
  [page chunk-size]
  (if (= page 0) 0 (* chunk-size page)))

(defn- calc-chunk-size
  [page chunk-size]
  (if (= page 0)
    chunk-size
    (* chunk-size 2)))

(defn- read-csv-chunk
  "Reads a chunk of a file and parses it as a CSV. The position and chunk-size are not guaranteed, since
   we shouldn't try to parse partial rows. We scan forward from the starting position to find the first
   line-ending and then scan backwards from the last position for the last line-ending."
  [user path-or-uuid page chunk-size separator uuid?]
  (otel/with-span [s ["read-csv-chunk"]]
    (irods/with-irods-exceptions {:use-icat-transaction false} irods
      (log/warn "[read-csv-chunk]" user path-or-uuid page chunk-size separator)
      (future (force (:jargon irods)))
      (validate irods
                [:user-exists user (cfg/irods-zone)])
      (let [path (ft/rm-last-slash
                   (if uuid?
                     @(rods/uuid->path irods path-or-uuid)
                     path-or-uuid))]
        (validate irods
                  [:path-exists path user (cfg/irods-zone)]
                  [:path-is-file path user (cfg/irods-zone)]
                  [:path-readable path user (cfg/irods-zone)])
        (let [page            (dec page)
              start-pg        (if (= page 0) 0 (dec page))
              full-chunk-size (calc-chunk-size page chunk-size)
              fsize           (deref (rods/file-size irods user (cfg/irods-zone) path))
              pages           (num-pages chunk-size fsize)
              position        (start-pos page chunk-size)
              load-pos        (start-pos start-pg chunk-size)]
          (log/debug "reading from" load-pos "for" full-chunk-size)

          (when-not (<= page pages)
            (throw+ {:error_code   "ERR_INVALID_PAGE"
                     :page         (str page)
                     :number-pages (str pages)}))

          (let [^String chunk   (trim-chunk (read-at-position @(:jargon irods) path load-pos full-chunk-size) chunk-size page pages)
                        the-csv (read-csv chunk separator)]
            (log/debug "trimmed chunk for page" (inc page) ":" chunk)
            (log/debug "parsed csv" the-csv)
            {:path         path
             :page         (str (inc page))
             :number-pages (str pages)
             :user         user
             :max-cols     (str (reduce #(if (>= %1 %2) %1 %2) (map count the-csv)))
             :chunk-size   (str (count (.getBytes chunk)))
             :file-size    (str fsize)
             :csv          the-csv}))))))

(with-pre-hook! #'read-csv-chunk
  (fn [user path-or-uuid page chunk-size separator uuid?]
    (when-not (pos? page)
      (throw+ {:error_code "ERR_PAGE_NOT_POS"
               :page       page}))
    (when-not (pos? chunk-size)
      (throw+ {:error_code "ERR_CHUNK_TOO_SMALL"
               :chunk-size (str chunk-size)}))))

(defn do-read-csv-chunk-uuid
  [{user :user separator :separator page :page size :size} data-id]
  (read-csv-chunk user data-id page size (url/url-decode separator) true))

(defn do-read-csv-chunk
  [{user :user separator :separator page :page size :size} path]
  (read-csv-chunk user path page size (url/url-decode separator) false))

(with-pre-hook! #'do-read-csv-chunk-uuid
  (fn [params data-id]
    (dul/log-call "do-read-csv-chunk-uuid" params data-id)))

(with-post-hook! #'do-read-csv-chunk-uuid
  (fn [result]
    (dul/log-result "do-read-csv-chunk-uuid" (dissoc result :csv))))

(with-pre-hook! #'do-read-csv-chunk
  (fn [params path]
    (dul/log-call "do-read-csv-chunk" params path)))

(with-post-hook! #'do-read-csv-chunk
  (fn [result]
    (dul/log-result "do-read-csv-chunk" (dissoc result :csv))))
