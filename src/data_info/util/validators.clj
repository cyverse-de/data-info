(ns data-info.util.validators
  (:use [kameleon.uuids :only [is-uuid?]]
        [slingshot.slingshot :only [throw+]])
  (:require [clojure.set :as set]
            [clojure.string :as str]
            [clj-icat-direct.icat :as icat]
            [clj-jargon.init :as init]
            [clj-jargon.item-info :as item]
            [clj-jargon.permissions :as perm]
            [clj-jargon.tickets :as tickets]
            [clj-jargon.users :as user]
            [clj-jargon.by-uuid :as uuid]
            [clojure-commons.error-codes :as error]
            [data-info.util.config :as cfg]
            [data-info.util.paths :as paths])
  (:import [clojure.lang IPersistentCollection]))


(defn ^Boolean good-string?
  "Checks that a string doesn't contain any problematic characters.

   Params:
     bad-chars - the characters that shouldn't be in the string
     to-check  - The string to check

   Returns:
     It returns false if the string contains at least one problematic character, otherwise false."
  ([^String to-check]
    (good-string? (seq (cfg/bad-chars)) to-check))
  ([^IPersistentCollection bad-chars ^String to-check]
    (let [chars-to-check (set (seq to-check))]
      (empty? (set/intersection (set bad-chars) chars-to-check)))))

(defn good-pathname
  [^String to-check]
  (when-not (good-string? to-check)
    (throw+ {:error_code error/ERR_BAD_OR_MISSING_FIELD :path to-check})))

(defn valid-bool-param
  "Validates that a given value is a Boolean.

   Parameters:
     param-name - the name of the param holding the proposed Boolean
     param-val  - the proposed Boolean

   Throws:
     It throws a map with of the following form.

       {:error_code ERR_BAD_REQUEST
        :param      param-name
        :value      param-val}"
  [^String param-name ^String param-val]
  (let [val (str/lower-case param-val)]
    (when-not (or (= val "true") (= val "false"))
      (throw+ {:error_code error/ERR_BAD_REQUEST
               :param      param-name
               :value      param-val}))))


(defn- num-paths-okay?
  [path-count]
  (<= path-count (cfg/max-paths-in-request)))


(defn- validate-path-count
  [count]
  (if-not (num-paths-okay? count)
    (throw+ {:error_code error/ERR_TOO_MANY_RESULTS
             :count count
             :limit (cfg/max-paths-in-request)})))


(defn validate-num-paths
  [paths]
  (validate-path-count (count paths)))


(defn validate-num-paths-under-folder
  [user folder]
  (let [total (icat/number-of-all-items-under-folder user (cfg/irods-zone) folder)]
    (validate-path-count total)))


(defn validate-num-paths-under-paths
  [user paths]
  (let [sum-fn #(+ %1 (icat/number-of-all-items-under-folder user (cfg/irods-zone) %2))
        total (reduce sum-fn 0 paths)]
    (validate-path-count total)))


(defn all-paths-exist
  [cm paths]
  (when-not (every? #(item/exists? cm %) paths)
    (throw+ {:error_code error/ERR_DOES_NOT_EXIST
             :paths      (filterv #(not (item/exists? cm  %1)) paths)})))


(defn no-paths-exist
  [cm paths]
  (when (some #(item/exists? cm %) paths)
    (throw+ {:error_code error/ERR_EXISTS
             :paths (filterv #(item/exists? cm %) paths)})))


(defn path-exists
  [cm path]
  (when-not (item/exists? cm path)
    (throw+ {:error_code error/ERR_DOES_NOT_EXIST
             :path path})))


(defn path-not-exists
  [cm path]
  (when (item/exists? cm path)
    (throw+ {:path path
             :error_code error/ERR_EXISTS})))


(defn all-paths-readable
  [cm user paths]
  (when-not (every? #(perm/is-readable? cm user %) paths)
    (throw+ {:error_code error/ERR_NOT_READABLE
             :path       (filterv #(not (perm/is-readable? cm user %)) paths)})))


(defn path-readable
  [cm user path]
  (when-not (perm/is-readable? cm user path)
    (throw+ {:error_code error/ERR_NOT_READABLE
             :path path
             :user user})))


(defn all-paths-writeable
  [cm user paths]
  (when-not (perm/paths-writeable? cm user paths)
    (throw+ {:paths      (filterv #(not (perm/is-writeable? cm user %)) paths)
             :error_code error/ERR_NOT_WRITEABLE})))


(defn path-writeable
  [cm user path]
  (when-not (perm/is-writeable? cm user path)
    (throw+ {:error_code error/ERR_NOT_WRITEABLE
             :path path})))


(defn path-is-dir
  [cm path]
  (when-not (item/is-dir? cm path)
    (throw+ {:error_code error/ERR_NOT_A_FOLDER
             :path path})))


(defn stat-is-dir
  [{:keys [path type]}]
  (when-not (= :dir type)
    (throw+ {:error_code error/ERR_NOT_A_FOLDER
             :path path})))


(defn path-is-file
  [cm path]
  (when-not (item/is-file? cm path)
    (throw+ {:error_code error/ERR_NOT_A_FILE
             :path path})))


(defn paths-are-files
  [cm paths]
  (when-not (every? #(item/is-file? cm %) paths)
    (throw+ {:error_code error/ERR_NOT_A_FILE
             :path       (filterv #(not (item/is-file? cm %)) paths)})))

(defn not-base-path
  [user path]
  (when (or (paths/user-trash-dir? user path)
            (paths/base-trash-path? path)
            (paths/sharing? path)   ;; currently the same as the base "home" path
            (paths/community? path))
    (throw+ {:error_code error/ERR_BAD_OR_MISSING_FIELD :path path})))

(defn all-users-exist
  [cm users]
  (when-not (every? #(user/user-exists? cm %) users)
    (throw+ {:error_code error/ERR_NOT_A_USER
             :users      (filterv #(not (user/user-exists? cm %1)) users)})))


(defn user-exists
  ([cm user]
    (when-not (user/user-exists? cm user)
      (throw+ {:error_code error/ERR_NOT_A_USER :user user})))

  ([user]
    (init/with-jargon (cfg/jargon-cfg) [cm]
      (user-exists cm user))))

(defn user-is-group-admin
  [cm username]
  (let [user (user/user cm username)]
    (when-not (some #{(:type user)} [:admin :group-admin])
      (throw+ {:error_code error/ERR_FORBIDDEN})))) ;; maybe a better code, but idk

(defn group-exists
  [cm group]
  (when-not (user/group-exists? cm group)
    (throw+ {:error_code error/ERR_DOES_NOT_EXIST
             :group group})))

(defn group-does-not-exist
  [cm group]
  (when (user/group-exists? cm group)
    (throw+ {:error_code error/ERR_EXISTS
             :group group})))

(defn user-owns-path
  [cm user path]
  (when-not (perm/owns? cm user path)
    (throw+ {:error_code error/ERR_NOT_OWNER
             :user user
             :path path})))


(defn user-owns-paths
  [cm user paths]
  (let [belongs-to? (partial perm/owns? cm user)]
    (when-not (every? belongs-to? paths)
      (throw+ {:error_code error/ERR_NOT_OWNER
               :user user
               :paths (filterv #(not (belongs-to? %)) paths)}))))

(defn all-uuids-exist
  [cm uuids]
  (when-not (every? #(uuid/get-path cm %) uuids)
    (throw+ {:error_code error/ERR_DOES_NOT_EXIST
             :ids      (filterv #(not (uuid/get-path cm  %1)) uuids)})))

(defn ticket-exists
  [cm user ticket-id]
  (when-not (tickets/ticket? cm (:username cm) ticket-id)
    (throw+ {:error_code error/ERR_TICKET_DOES_NOT_EXIST
             :user user
             :ticket-id ticket-id})))

(defn ticket-does-not-exist
  [cm user ticket-id]
  (when (tickets/ticket? cm (:username cm) ticket-id)
    (throw+ {:error_code error/ERR_TICKET_EXISTS
             :user user
             :ticket-id ticket-id})))

(defn all-tickets-exist
  [cm user ticket-ids]
  (when-not (every? #(tickets/ticket? cm (:username cm) %) ticket-ids)
    (throw+ {:ticket-ids (filterv #(not (tickets/ticket? cm (:username cm) %)) ticket-ids)
             :error_code error/ERR_TICKET_DOES_NOT_EXIST})))

(defn all-tickets-nonexistant
  [cm user ticket-ids]
  (when (some #(tickets/ticket? cm (:username cm) %) ticket-ids)
    (throw+ {:ticket-ids (filterv #(tickets/ticket? cm (:username cm) %) ticket-ids)
             :error_code error/ERR_TICKET_EXISTS})))
