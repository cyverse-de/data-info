(ns data-info.services.groups
  (:require [clojure.tools.logging :as log]
            [slingshot.slingshot :refer [throw+]]
            [clj-jargon.users :as users]
            [clojure.string :as string]
            [clojure.set :as cset]
            [data-info.services.users :as di-users]
            [data-info.util.config :as cfg]
            [data-info.util.irods :as irods]
            [data-info.util.validators :as validators]))

(defn get-group
  [{user :user} group-name]
  (irods/with-jargon-exceptions [cm]
    (validators/user-exists cm user)
    (validators/group-exists cm group-name)
    {:name group-name :members (users/list-group-members cm group-name)}))

(defn create-group
  [{user :user} {group-name :name members :members}]
  (irods/with-jargon-exceptions [cm]
    (validators/user-exists cm user)
    (validators/user-is-group-admin cm user)
    (validators/group-does-not-exist cm group-name)
    (validators/all-users-exist cm members)
    (users/create-user-group cm group-name)
    (when (seq members)
      (dorun (map
               (partial users/add-to-group cm group-name)
               members)))
    {:name group-name :members (users/list-group-members cm group-name)}))

(defn update-group-members
  [{user :user :as params} {members :members} group-name]
  (irods/with-jargon-exceptions [cm]
    (validators/user-exists cm user)
    (validators/user-is-group-admin cm user)
    (validators/group-exists cm group-name)
    ;; instead of this, we filter the list by extant users, so the group can
    ;; still be updated if a few don't exist in irods
    ;(validators/all-users-exist cm members)
    (let [current-members (set (users/list-group-members cm group-name))
          desired-members (set (map di-users/ensure-qualified (filter users/user-exists? members)))

          members-to-add (cset/difference desired-members current-members)
          members-to-remove (cset/difference current-members desired-members)

          unqualify (fn [name] (string/replace name (re-pattern (str "#\\Q" (:zone cm) "\\E$")) ""))
          addfn (fn [user] (users/add-to-group cm group-name (unqualify user)))
          rmfn (fn [user] (users/remove-from-group cm group-name (unqualify user)))]
      (doall (map addfn members-to-add))
      (doall (map rmfn members-to-remove))
      {:name group-name :members (users/list-group-members cm group-name)})))

(defn delete-group
  [{user :user} group-name]
  (irods/with-jargon-exceptions [cm]
    (validators/user-exists cm user)
    (validators/user-is-group-admin cm user)
    (when (users/group-exists? cm group-name)
      (users/delete-user-group cm group-name))
    nil))
