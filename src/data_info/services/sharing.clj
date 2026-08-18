(ns data-info.services.sharing
  (:use [clj-jargon.item-info :only [trash-base-dir is-dir?]]
        [clj-jargon.permissions]
        [slingshot.slingshot :only [try+ throw+]])
  (:require [clojure.tools.logging :as log]
            [clojure.string :as string]
            [clojure-commons.error-codes :as error]
            [clojure-commons.file-utils :as ft]
            [clj-irods.core :as rods]
            [cemerick.url :as url]
            [dire.core :refer [with-pre-hook! with-post-hook!]]
            [data-info.util.logging :as dul]
            [data-info.util.paths :as paths]
            [data-info.util.config :as cfg]
            [data-info.util.irods :as irods]
            [data-info.util.validators :as validators])
  (:import [java.io IOException]
           [java.net URLEncoder]
           [org.irods.jargon.core.exception JargonException]))

(defn- shared?
  ([cm share-with fpath]
     (:read (permissions cm share-with fpath)))
  ([cm share-with fpath desired-perm]
     (let [curr-perm (permission-for cm share-with fpath)]
       (= curr-perm desired-perm))))

(defn- skip-share
  [user path reason]
  (log/warn "Skipping share of" path "with" user "because:" reason)
  {:user    user
   :path    path
   :reason  reason
   :skipped true})

(defn- share-path-home
  "Returns the home directory that a shared file is under."
  [share-path]
  (string/join "/" (take 4 (string/split share-path #"\/"))))

(defn- share-path*
  "Shares a path with a user. This consists of the following steps:

       1. The parent directories up to the sharer's home directory need to be marked as readable
          by the sharee. Othwerwise, any files that are shared will be orphaned in the UI.

       2. If the shared item is a directory then the inherit bit needs to be set so that files
          that are uploaded into the directory will also be shared.

       3. The permissions are set on the item being shared. This is done recursively in case the
          item being shared is a directory."
  [cm user share-with perm fpath]
  (let [hdir      (share-path-home fpath)
        trash-dir (trash-base-dir (:zone cm))
        base-dirs #{hdir trash-dir}]
    (log/warn fpath "is being shared with" share-with "by" user)
    (process-parent-dirs (partial set-readable cm share-with true) #(not (base-dirs %)) fpath)

    (when (is-dir? cm fpath)
      (log/warn fpath "is a directory, setting the inherit bit.")
      (set-inherits cm fpath))

    (when-not (is-readable? cm share-with hdir)
      (log/warn share-with "is being given read permissions on" hdir "by" user)
      (set-permission cm share-with hdir :read false))

    (log/warn share-with "is being given recursive permissions (" perm ") on" fpath)
    (set-permission cm share-with fpath (keyword perm) true)

    {:user share-with :path fpath}))

(defn share-path
  [cm user share-with fpath perm]
  (cond (= user share-with)                (skip-share share-with fpath :share-with-self)
        (paths/in-trash? user fpath)       (skip-share share-with fpath :share-from-trash)
        (shared? cm share-with fpath perm) (skip-share share-with fpath :already-shared)
        :else                              (share-path* cm user share-with perm fpath)))

(defn- share-paths
  [cm user share-withs fpaths perm]
  (for [share-with share-withs
        fpath      fpaths]
    (share-path cm user share-with fpath perm)))

(defn- share
  [cm user share-withs fpaths perm]
  (validators/user-exists cm user)
  (validators/all-users-exist cm share-withs)
  (validators/all-paths-exist cm fpaths)
  (validators/user-owns-paths cm user fpaths)

  (let [keyfn      #(if (:skipped %) :skipped :succeeded)
        share-recs (group-by keyfn (share-paths cm user share-withs fpaths perm))
        sharees    (map :user (:succeeded share-recs))
        home-dir   (paths/user-home-dir user)]
    {:user        sharees
     :path        fpaths
     :skipped     (map #(dissoc % :skipped) (:skipped share-recs))
     :permission  perm}))

(defn- remove-inherit-bit?
  [cm user fpath]
  (empty? (remove (comp (conj (set (cfg/irods-admins)) user) :user)
                  (list-user-perms cm fpath))))

(defn- unshare-dir
  "Removes the inherit bit from a directory if the directory is no longer shared with any accounts
   other than iRODS administrative accounts."
  [cm user unshare-with fpath]
  (when (remove-inherit-bit? cm user fpath)
    (log/warn "Removing inherit bit on" fpath)
    (remove-inherits cm fpath)))

(defn- unshare-path*
  "Removes permissions for a user to access a path.  This consists of several steps:

       1. Remove the access permissions for the user.  This is done recursively in case the path
          being unshared is a directory.

       2. If the item being unshared is a directory, perform any directory-specific unsharing
          steps that are required.

       3. Remove the user's read permissions for parent directories in which the user no longer has
          access to any other files or subdirectories."
  [cm user unshare-with fpath]
  (let [trash-base (trash-base-dir (:zone cm))
        path-base  (share-path-home fpath)
        base-dirs #{path-base trash-base}]
    (log/warn "Removing permissions on" fpath "from" unshare-with "by" user)
    (remove-permissions cm unshare-with fpath)

    (when (is-dir? cm fpath)
      (log/warn "Unsharing directory" fpath "from" unshare-with "by" user)
      (unshare-dir cm user unshare-with fpath))

    (log/warn "Removing read perms on parents of" fpath "from" unshare-with "by" user)
    (process-parent-dirs
     (partial set-readable cm unshare-with false)
     #(not (or (base-dirs %) (contains-accessible-obj? cm unshare-with %)))
     fpath)
    {:user unshare-with :path fpath}))

(defn unshare-path
  [cm user unshare-with fpath]
  (cond (= user unshare-with)           (skip-share unshare-with fpath :unshare-with-self)
        (shared? cm unshare-with fpath) (unshare-path* cm user unshare-with fpath)
        :else                           (skip-share unshare-with fpath :not-shared)))

(defn anon-readable?
  [irods p]
  (let [perm (rods/permission irods (cfg/anon-user) (cfg/irods-zone) p)]
    (delay (contains? #{:read :write :own} @perm))))

(defn- map-anon-url-path
  [path]
  (let [mappings (cfg/anon-files-mappings)
        mapping-keys (reverse (sort-by count (keys mappings)))
        matching-key (first (filter
                              (fn [key] (and
                                          (> (count path) (count key))
                                          (= key (subs path 0 (count key)))))
                              mapping-keys))]
    (when-not (nil? matching-key)
      (string/replace-first path matching-key (mappings matching-key)))))

(defn- encode-mapped-anon-path
  "Take a path that's been appropriately mapped, and URL encode it for use. This function will use %20 for spaces."
  [mapped-path]
  (as-> mapped-path p
    (string/split p #"/")
    (map #(string/replace (URLEncoder/encode %) #"\+" "%20") p)
    (string/join "/" p)))

(defn anon-file-url
  [path]
  (let [aurl (url/url (cfg/anon-files-base-url))
        encoded-path (encode-mapped-anon-path (map-anon-url-path path))]
    (-> aurl
        (assoc :path (ft/path-join (:path aurl) encoded-path))
        str)))

(defn- anon-files-urls
  [paths]
  (into {} (map #(vector %1 (anon-file-url %1)) paths)))

(defn- anon-files
  [user paths]
  (irods/with-jargon-exceptions [cm]
    (validators/user-exists cm user)
    (validators/all-paths-exist cm paths)
    (validators/paths-are-files cm paths)
    (validators/user-owns-paths cm user paths)
    (log/warn "Giving read access to" (cfg/anon-user) "on:" (string/join " " paths))
    (share cm user [(cfg/anon-user)] paths :read)
    {:user user :paths (anon-files-urls paths)}))

(defn do-anon-files
  [{:keys [user]} {:keys [paths]}]
  (anon-files user (mapv ft/rm-last-slash paths)))

(with-pre-hook! #'do-anon-files
  (fn [params body]
    (dul/log-call "do-anon-files" params body)
    (validators/validate-num-paths (:paths body))))

(with-post-hook! #'do-anon-files (dul/log-func "do-anon-files"))

(defn- outcome->item
  "Folds the result of a single share or unshare back into the request item it came from. A skip is
   reported as a success, matching what callers have always seen, with the reason kept alongside it."
  [item outcome]
  (cond-> (assoc item :success true)
    (:skipped outcome) (assoc :reason (name (:reason outcome)))))

(defn- per-path-failure
  "Renders a caught failure into the error details for a single path, or nil when the failure has to
   fail the request as a whole. Validators throw maps naming the path they rejected, and Jargon
   throws when iRODS refuses an operation - except that an IOException underneath it means iRODS
   itself is unreachable, which is not something the next path will do any better with."
  [e]
  (cond
    (map? e)
    e

    (and (instance? JargonException e) (not (instance? IOException (.getCause ^JargonException e))))
    {:error_code error/ERR_REQUEST_FAILED :reason (.getMessage ^JargonException e)}))

(defn- apply-to-path
  "Applies one share or unshare to a single path. Per-path failures are captured in the returned item
   rather than thrown, so that a path the requesting user doesn't own fails only its own entry."
  [cm user other-user share-fn {:keys [path] :as item}]
  (try+
   (validators/path-exists cm path)
   (validators/user-owns-path cm user path)
   (outcome->item item (share-fn item))
   (catch Object e
     (if-let [details (per-path-failure e)]
       (do (log/warn "failed to change the sharing of" path "with" other-user "by" user "-" e)
           (assoc item :success false :error details))
       (throw+)))))

(defn- missing-user
  "Returns the failure that fails a whole entry when the user it names doesn't exist, or nil when the
   user is usable. Checked once per entry rather than once per path, since each check is a lookup."
  [cm username]
  (try+
   (validators/user-exists cm username)
   nil
   (catch map? e e)))

(defn- apply-to-user
  "Applies a share or unshare to every path in one user's entry. A user who doesn't exist fails that
   entry's paths without touching the rest of the request."
  [cm user other-user share-fn items]
  (if-let [e (missing-user cm other-user)]
    (mapv #(assoc % :success false :error e) items)
    (mapv (partial apply-to-path cm user other-user share-fn) items)))

(defn do-share
  [{:keys [user]} {:keys [sharing]}]
  (irods/with-jargon-exceptions [cm]
    (validators/user-exists cm user)
    {:sharing (mapv (fn [{share-with :user paths :paths}]
                      (let [share-fn (fn [{:keys [path permission]}]
                                       (share-path cm user share-with path permission))]
                        {:user    share-with
                         :sharing (apply-to-user cm user share-with share-fn
                                                 (mapv #(update % :path ft/rm-last-slash) paths))}))
                    sharing)}))

(with-pre-hook! #'do-share
  (fn [params body]
    (dul/log-call "do-share" params body)
    (validators/validate-num-paths (mapcat :paths (:sharing body)))))

(with-post-hook! #'do-share (dul/log-func "do-share"))

(defn do-unshare
  [{:keys [user]} {:keys [unshare]}]
  (irods/with-jargon-exceptions [cm]
    (validators/user-exists cm user)
    {:unshare (mapv (fn [{unshare-with :user paths :paths}]
                      (let [unshare-fn (fn [{:keys [path]}]
                                         (unshare-path cm user unshare-with path))]
                        {:user    unshare-with
                         :unshare (apply-to-user cm user unshare-with unshare-fn
                                                 (mapv #(hash-map :path (ft/rm-last-slash %)) paths))}))
                    unshare)}))

(with-pre-hook! #'do-unshare
  (fn [params body]
    (dul/log-call "do-unshare" params body)
    (validators/validate-num-paths (mapcat :paths (:unshare body)))))

(with-post-hook! #'do-unshare (dul/log-func "do-unshare"))
