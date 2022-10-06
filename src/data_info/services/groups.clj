(ns data-info.services.groups
  (:require [clojure.tools.logging :as log]
            [slingshot.slingshot :refer [throw+]]
            [clj-jargon.users :as users]
            [clojure.string :as string]
            [data-info.services.users :as di-users]
            [data-info.util.config :as cfg]
            [data-info.util.irods :as irods]))

(defn get-group
  [{user :user} group-name]
  (irods/with-jargon-exceptions [cm]
    ;; validate
    {:group-name group-name :members (users/list-group-members cm group-name)}))

(defn create-group
  [{user :user} {group-name :group-name members :members}]
  (irods/with-jargon-exceptions [cm]
    ;; need to validate
    (users/create-user-group cm group-name)
    (when (seq members)
      (dorun (map
               (partial users/add-to-group cm group-name)
               members)))
    {:group-name group-name :members (users/list-group-members cm group-name)}))

(defn update-group-members
  [{user :user :as params} {members :members} group-name]
  (irods/with-jargon-exceptions [cm]
    ;; validate
    (let [current-members (users/list-group-members cm group-name)]
      (loop [old-members (sort current-members)
             new-members (sort members)]
        ;; compare the head of both lists and determine if anything needs adding or removing
        (let [c (first old-members)
              n (first new-members)]
          (cond
            (and (nil? c) (nil? n))
            nil ;; both lists are exhausted, we're done!

            (or (nil? c) (and
                           (not (nil? n))
                           (neg? (compare (di-users/qualify-username n) c)))) ;; the new member isn't in the current members, should be added
            (do
              (users/add-to-group cm group-name n)
              (recur old-members (rest new-members)))

            (or (nil? n) (and
                           (not (nil? c))
                           (neg? (compare c (di-users/qualify-username n))))) ;; the old member isn't in the new list, should be removed
            (do
              (users/remove-from-group cm group-name (string/replace c (re-pattern (str "#" (:zone cm) "$")) ""))
              (recur (rest old-members) new-members))

            (= c (di-users/qualify-username n)) ;; already correct for both of these
            (recur (rest old-members) (rest new-members))

            :else
            (throw+ {:error "Couldn't figure out what to do with users" :new n :old c}))))
      {:group-name group-name :members (users/list-group-members cm group-name)})))

(defn delete-group
  [{user :user} group-name]
  (irods/with-jargon-exceptions [cm]
    ;; validate
    (users/delete-user-group cm group-name)
    nil))
