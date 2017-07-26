(ns data-info.services.path-lists-tests
  (:use [clojure.test]))

;; Re-def private functions so they can be tested in this namespace.
(def keep-top-level-file? #'data-info.services.path-lists/keep-top-level-file?)
(def label-matches?       #'data-info.services.path-lists/label-matches?)
(def keep-data-item?      #'data-info.services.path-lists/keep-data-item?)


(def top-level-files [{:type       :file,
                       :path       "/root/no-perms.csv",
                       :label      "no-perms.csv",
                       :infoType   "csv"}
                      {:type       :file,
                       :path       "/root/file-top.txt",
                       :label      "file-top.txt",
                       :infoType   "raw",
                       :permission :read}
                      {:type       :file,
                       :path       "/root/file-top.csv",
                       :label      "file-top.csv",
                       :infoType   "csv",
                       :permission :read}])

(def data-items (concat top-level-files
                        [{:type       :dir,
                          :path       "/root/folder-1",
                          :label      "folder-1",
                          :permission :read}
                         {:type       :dir,
                          :path       "/root/folder-1/no-perms",
                          :label      "no-perms"}
                         {:type       :dir,
                          :path       "/root/folder-1/sub1",
                          :label      "sub1",
                          :permission :read}
                         {:type       :dir,
                          :path       "/root/folder-1/sub1/sub2",
                          :label      "sub2",
                          :permission :read}
                         {:type       :dir,
                          :path       "/root/folder-1/not-a-csv-folder",
                          :label      "not-a-csv-folder",
                          :permission :read}
                         {:type       :file,
                          :path       "/root/folder-1/no-perms/no-perms-sub.csv",
                          :infoType   "csv",
                          :label      "no-perms-sub.csv"}
                         {:type       :file,
                          :path       "/root/folder-1/file-1.txt",
                          :label      "file-1.txt",
                          :infoType   "raw",
                          :permission :read}
                         {:type       :file,
                          :path       "/root/folder-1/not-a-csv-file",
                          :label      "not-a-csv-file",
                          :infoType   "raw",
                          :permission :read}
                         {:type       :file,
                          :path       "/root/folder-1/sub1/file-1.csv",
                          :label      "file-1.csv",
                          :infoType   "csv",
                          :permission :read}
                         {:type       :file,
                          :path       "/root/folder-1/sub1/file-2.txt",
                          :label      "file-2.txt",
                          :infoType   "raw",
                          :permission :read}
                         {:type       :file,
                          :path       "/root/folder-1/sub1/sub2/file-2.csv",
                          :label      "file-2.csv",
                          :infoType   "csv",
                          :permission :read}]))

(defn- top-level-files->keep-top-level-file?->set
  [info-types]
  (->> top-level-files
       (filter (keep-top-level-file? info-types))
       (map :path)
       set))

(defn- data-items->label-matches?->set
  [name-pattern]
  (->> data-items
       (filter #(label-matches? name-pattern (:label %)))
       (map :path)
       set))

(defn- data-items->keep-data-item?->set
  [name-pattern folders-only?]
  (->> data-items
       (filter (partial keep-data-item? name-pattern folders-only?))
       (map :path)
       set))


(deftest test-keep-top-level-file?
  (testing "keep-top-level-file? nil or empty info-types"
    (is (= (top-level-files->keep-top-level-file?->set [])
           (set (map :path top-level-files))))

    (is (= (top-level-files->keep-top-level-file?->set nil)
           (set (map :path top-level-files)))))

  (testing "keep-top-level-file? csv"
    (is (= (top-level-files->keep-top-level-file?->set ["csv"])
           #{"/root/no-perms.csv"
             "/root/file-top.csv"})))

  (testing "keep-top-level-file? raw"
    (is (= (top-level-files->keep-top-level-file?->set ["raw"])
           #{"/root/file-top.txt"}))))


(deftest test-label-matches?
  (testing "label-matches? nil or blank"
    (is (= (data-items->label-matches?->set nil)
           (set (map :path data-items))))

    (is (= (data-items->label-matches?->set "")
           (set (map :path data-items)))))

  (testing "label-matches? \\.txt$"
    (is (= (data-items->label-matches?->set "\\.txt$")
           #{"/root/file-top.txt"
             "/root/folder-1/file-1.txt"
             "/root/folder-1/sub1/file-2.txt"})))

  (testing "label-matches? ^file"
    (is (= (data-items->label-matches?->set "^file")
           #{"/root/file-top.txt"
             "/root/file-top.csv"
             "/root/folder-1/file-1.txt"
             "/root/folder-1/sub1/file-1.csv"
             "/root/folder-1/sub1/file-2.txt"
             "/root/folder-1/sub1/sub2/file-2.csv"})))

  (testing "label-matches? sub"
    (is (= (data-items->label-matches?->set "sub")
           #{"/root/folder-1/sub1"
             "/root/folder-1/sub1/sub2"
             "/root/folder-1/no-perms/no-perms-sub.csv"})))

  (testing "label-matches? csv"
    (is (= (data-items->label-matches?->set "csv")
           #{"/root/folder-1/not-a-csv-folder"
             "/root/no-perms.csv"
             "/root/file-top.csv"
             "/root/folder-1/no-perms/no-perms-sub.csv"
             "/root/folder-1/not-a-csv-file"
             "/root/folder-1/sub1/file-1.csv"
             "/root/folder-1/sub1/sub2/file-2.csv"}))))


(deftest test-keep-data-item?
  (testing "keep-data-item? all files"
    (is (= (data-items->keep-data-item?->set "" false)
           #{"/root/file-top.txt"
             "/root/file-top.csv"
             "/root/folder-1/file-1.txt"
             "/root/folder-1/not-a-csv-file"
             "/root/folder-1/sub1/file-1.csv"
             "/root/folder-1/sub1/file-2.txt"
             "/root/folder-1/sub1/sub2/file-2.csv"})))

  (testing "keep-data-item? all folders"
    (is (= (data-items->keep-data-item?->set nil true)
           #{"/root/folder-1"
             "/root/folder-1/not-a-csv-folder"
             "/root/folder-1/sub1"
             "/root/folder-1/sub1/sub2"})))

  (testing "keep-data-item? sub folders"
    (is (= (data-items->keep-data-item?->set "sub" true)
           #{"/root/folder-1/sub1"
             "/root/folder-1/sub1/sub2"})))

  (testing "keep-data-item? \\.txt$ files"
    (is (= (data-items->keep-data-item?->set "\\.txt$" false)
           #{"/root/file-top.txt"
             "/root/folder-1/file-1.txt"
             "/root/folder-1/sub1/file-2.txt"})))

  (testing "keep-data-item? csv files"
    (is (= (data-items->keep-data-item?->set "csv" false)
           #{"/root/file-top.csv"
             "/root/folder-1/not-a-csv-file"
             "/root/folder-1/sub1/file-1.csv"
             "/root/folder-1/sub1/sub2/file-2.csv"}))))
