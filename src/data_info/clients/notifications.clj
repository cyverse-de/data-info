(ns data-info.clients.notifications
  (:require [cheshire.core :as json]
            [cemerick.url :as url]
            [clj-http.client :as http]
            [clojure-commons.file-utils :as ft]
            [clojure.string :as string]
            [data-info.util.paths :as paths]
            [data-info.util.config :as config]))

(defn send-notification
  "Sends a notification to a user"
  [message]
  (let [notification-url (str (-> (url/url (config/notificationagent-base-url)) (assoc :path "/notification")))
        res (http/post notification-url
                       {:content-type :json
                        :body (json/encode message)})]
    res))

(defn- amend-notification
  [notification user action start-paths end-paths]
  (assoc notification
         :type "data"
         :user user
         :payload {:action action
                   :paths end-paths}))

(defn- completed-text
  [failed?]
  (if failed? "Failed" "Finished"))

(defn move-notification
  [user start-paths end-paths failed?]
  (amend-notification
    {:subject (str (completed-text failed?) " moving " (count start-paths) " file(s)/folder(s) to " (first end-paths))
     :message (str (completed-text failed?) " moving " (string/join ", " start-paths) " to " (first end-paths))} 
    user "move" start-paths end-paths))

(defn rename-notification
  [user start-paths end-paths failed?]
  (amend-notification
    {:subject (str (completed-text failed?) " renaming " (first start-paths) " to " (ft/basename (first end-paths)))
     :message (str (completed-text failed?) " renaming " (first start-paths) " to " (first end-paths))}
    user "rename" start-paths end-paths))

(defn trash-notification
  [user start-paths failed?]
  (amend-notification
    {:subject (str (completed-text failed?) " moving " (count start-paths) " files(s)/folder(s) to trash")
     :message (str (completed-text failed?) " moving " (string/join ", " start-paths) " to trash")}
    user "trash" start-paths [(paths/user-trash-path user)]))

(defn restore-notification
  [user start-paths end-paths failed?]
  (amend-notification
    {:subject (str (completed-text failed?) " restoring " (count start-paths) " files(s)/folder(s) to their original locations")
     :message (str (completed-text failed?) " restoring " (string/join ", " start-paths) " to " (string/join ", " end-paths))}
    user "restore" start-paths end-paths))

(defn empty-trash-notification
  [user failed?]
  {:type "data"
   :user user
   :subject (str (completed-text failed?) " emptying trash")
   :message (str (completed-text failed?) " emptying trash")
   :payload {:action "empty_trash"
             :paths [(paths/user-trash-path user)]}})
