(ns data-info.routes.data
  (:use [common-swagger-api.routes]
        [common-swagger-api.schema]
        [data-info.routes.schemas.common]
        [data-info.routes.schemas.data]
        [data-info.routes.schemas.stats])
  (:require [data-info.services.create :as create]
            [data-info.services.metadata :as meta]
            [data-info.services.manifest :as manifest]
            [clojure.tools.logging :as log]
            [data-info.services.entry :as entry]
            [data-info.services.write :as write]
            [data-info.services.page-file :as page-file]
            [data-info.services.page-tabular :as page-tabular]
            [data-info.services.uuids :as uuids]
            [data-info.util.config :as cfg]
            [schema.core :as s]
            [clojure-commons.error-codes :as ce]
            [data-info.util.service :as svc]))

(defroutes data-operations

  (context "/data" []
    :tags ["data"]

    (GET "/uuid" [:as {uri :uri}]
      :query [params PathToUUIDParams]
      :return PathToUUIDReturn
      :summary "Get the UUID for a path"
      :description (str "Get a UUID for a path provided as a query parameter."
(get-error-code-block "ERR_DOES_NOT_EXIST, ERR_NOT_READABLE, ERR_NOT_A_USER"))
      (svc/trap uri uuids/do-simple-uuid-for-path params))

    (GET "/path/:zone/*" [:as {{zone :zone path :*} :params uri :uri}]
      :query [params FolderListingParams]
      :no-doc true
      (ce/trap uri entry/dispatch-path-to-resource zone path params))

    ;; This is actually handled by the above route, which cannot be documented properly.
    (GET "/path/:zone/:path" [:as {uri :uri}]
      :path-params [zone :- (describe String "The IRODS zone")
                    path :- (describe String "The IRODS path under the zone")]
      :query [params FolderListingParams]
      :summary "Data Item Contents"
      :description (str
"Lists subdirectories and file details of directory paths, or gets file contents of paths to files.

 Of the optional query parameters, only the `attachment` parameter applies to files, and all others
 only apply to listing directory contents.

 The `limit` parameter is required for paths to directories."
(get-error-code-block
  "ERR_DOES_NOT_EXIST, ERR_NOT_READABLE, ERR_NOT_A_USER,"
  "ERR_BAD_PATH_LENGTH, ERR_BAD_DIRNAME_LENGTH, ERR_BAD_BASENAME_LENGTH"
  "ERR_BAD_QUERY_PARAMETER, ERR_MISSING_QUERY_PARAMETER"))
      {:status 501})

    (POST "/" [:as {uri :uri}]
      :query [params FileUploadQueryParams]
      :multipart-params [file :- s/Any]
      :middleware [write/wrap-multipart-create]
      :return FileStat
      :summary "Upload a file"
      :description (str
"Uploads a file into a directory as a user, given the directory exists and is writeable but the file does not exist."
(get-error-code-block "ERR_NOT_A_USER, ERR_EXISTS, ERR_DOES_NOT_EXIST, ERR_NOT_WRITEABLE"))
      (svc/trap uri write/do-upload params file))

    (POST "/directories" [:as {uri :uri}]
      :tags ["bulk"]
      :query [params StandardUserQueryParams]
      :body [body (describe Paths "The paths to create.")]
      :return Paths
      :summary "Create Directories"
      :description (str
"Creates a directory, as well as any intermediate directories that do not already exist, given as a
path in the request. For example, if the path `/tempZone/home/rods/test1/test2/test3` is given in
the request, the `/tempZone/home/rods/test1` directory does not exist, and the requesting user has
write permissions on the `/tempZone/home/rods` folder, then all 3 `test*` folders will be created
for the requesting user. This endpoint will throw ERR_BAD_OR_MISSING_FIELD when it is given names
with characters in a runtime-configurable parameter. Currently, this parameter lists: " (vec (cfg/bad-chars)) "."
(get-error-code-block
  "ERR_BAD_OR_MISSING_FIELD, ERR_NOT_WRITEABLE, ERR_EXISTS, ERR_DOES_NOT_EXIST, ERR_NOT_A_USER"))
      (svc/trap uri create/do-create params body))

    (context "/by-path" []
      :tags ["data"]

      ; add both versions to catch multiple types of path passing
      (GET "/manifest/*" [:as {{path :*} :params uri :uri}]
        :query [{:keys [user]} StandardUserQueryParams]
        :no-doc true
        (svc/trap uri manifest/do-manifest user (str "/" path)))

      (GET "/manifest/:path" [:as {uri :uri}]
        :query [{:keys [user]} StandardUserQueryParams]
        :path-params [path :- String]
        :return Manifest
        :summary "Return file manifest"
        :description (str
"Returns a manifest for a file."
(get-error-code-block "ERR_NOT_A_USER, ERR_DOES_NOT_EXIST, ERR_NOT_A_FILE, ERR_NOT_READABLE"))
        (svc/trap uri manifest/do-manifest user (str "/" path)))

      (GET "/chunks/*" [:as {{path :*} :params uri :uri}]
        :query [params ChunkParams]
        :no-doc true
        (svc/trap uri page-file/do-read-chunk params (str "/" path)))

      (GET "/chunks/:path" [:as {uri :uri}]
        :query [params ChunkParams]
        :path-params [path :- String]
        :return ChunkReturn
        :summary "Get File Chunk"
        :description (str
  "Gets the chunk of the file of the specified position and size."
  (get-error-code-block
    "ERR_DOES_NOT_EXIST, ERR_NOT_A_FILE, ERR_NOT_READABLE, ERR_NOT_A_USER"))
        (svc/trap uri page-file/do-read-chunk params (str "/" path)))

      (GET "/chunks-tabular/*" [:as {{path :*} :params uri :uri}]
        :query [params TabularChunkParams]
        :no-doc true
        (svc/trap uri page-tabular/do-read-csv-chunk params (str "/" path)))

      (GET "/chunks-tabular/:path" [:as {uri :uri}]
        :query [params TabularChunkParams]
        :path-params [path :- String]
        :return (doc-only TabularChunkReturn TabularChunkDoc)
        :summary "Get Tabular File Chunk"
        :description (str
  "Gets the specified page of the tabular file, with a page size roughly corresponding to the provided size. The size is not precisely guaranteed, because partial lines cannot be correctly parsed."
  (get-error-code-block
    "ERR_DOES_NOT_EXIST, ERR_NOT_A_FILE, ERR_NOT_READABLE, ERR_NOT_A_USER, ERR_INVALID_PAGE, ERR_PAGE_NOT_POS, ERR_CHUNK_TOO_SMALL"))
        (svc/trap uri page-tabular/do-read-csv-chunk params path)))

    (context "/:data-id" []
      :path-params [data-id :- DataIdPathParam]
      :tags ["data-by-id"]

      (HEAD "/" [:as {uri :uri}]
        :query [{:keys [user]} StandardUserQueryParams]
        :responses {200 {:description "User has read permissions for given data item."}
                    403 {:description "User does not have read permissions for given data item."}
                    404 {:description "Data Item ID does not exist."}
                    422 {:description "User does not exist or an internal error occurred."}}
        :summary "Data Item Meta-Status"
        :description "Returns an HTTP status according to the user's access level to the data item."
        (ce/trap uri entry/id-entry data-id user))

      (PUT "/" [:as {uri :uri}]
        :query [params StandardUserQueryParams]
        :multipart-params [file :- s/Any]
        :middleware [write/wrap-multipart-overwrite]
        :return FileStat
        :summary "Overwrite Contents"
        :description (str
"Overwrites a file as a user, given the user can write to it and the file already exists."
(get-error-code-block "ERR_NOT_A_USER, ERR_DOES_NOT_EXIST, ERR_NOT_A_FILE, ERR_NOT_WRITEABLE"))
        (svc/trap uri write/do-upload params file))

      (GET "/manifest" [:as {uri :uri}]
        :query [{:keys [user]} StandardUserQueryParams]
        :return Manifest
        :summary "Return file manifest"
        :description (str
"Returns a manifest for a file."
(get-error-code-block "ERR_NOT_A_USER, ERR_DOES_NOT_EXIST, ERR_NOT_A_FILE, ERR_NOT_READABLE"))
        (svc/trap uri manifest/do-manifest-uuid user data-id))

      (GET "/chunks" [:as {uri :uri}]
        :query [params ChunkParams]
        :return ChunkReturn
        :summary "Get File Chunk"
        :description (str
  "Gets the chunk of the file of the specified position and size."
  (get-error-code-block
    "ERR_DOES_NOT_EXIST, ERR_NOT_A_FILE, ERR_NOT_READABLE, ERR_NOT_A_USER"))
        (svc/trap uri page-file/do-read-chunk-uuid params data-id))

      (GET "/chunks-tabular" [:as {uri :uri}]
        :query [params TabularChunkParams]
        :return (doc-only TabularChunkReturn TabularChunkDoc)
        :summary "Get Tabular File Chunk"
        :description (str
  "Gets the specified page of the tabular file, with a page size roughly corresponding to the provided size. The size is not precisely guaranteed, because partial lines cannot be correctly parsed."
  (get-error-code-block
    "ERR_DOES_NOT_EXIST, ERR_NOT_A_FILE, ERR_NOT_READABLE, ERR_NOT_A_USER, ERR_INVALID_PAGE, ERR_PAGE_NOT_POS, ERR_CHUNK_TOO_SMALL"))
        (svc/trap uri page-tabular/do-read-csv-chunk-uuid params data-id))

      (POST "/metadata/save" [:as {uri :uri}]
        :query [params StandardUserQueryParams]
        :body [body (describe MetadataSaveRequest "The metadata save request.")]
        :return FileStat
        :summary "Exporting Metadata to a File"
        :description (str
  "Exports file/folder details in a JSON format (similar to the /stat-gatherer endpoint response),
  including all Metadata Template AVUs and IRODS AVUs visible to the requesting user, to the file
  specified in the request."
  (get-error-code-block
    "ERR_INVALID_JSON, ERR_EXISTS, ERR_DOES_NOT_EXIST, ERR_NOT_READABLE,"
    "ERR_NOT_WRITEABLE, ERR_NOT_A_USER, ERR_BAD_PATH_LENGTH, ERR_BAD_DIRNAME_LENGTH,"
    "ERR_BAD_BASENAME_LENGTH, ERR_TOO_MANY_RESULTS")
  (get-endpoint-delegate-block
    "metadata"
    "GET /avus/{target-type}/{target-id}"))
        (svc/trap uri meta/do-metadata-save data-id params body))

      (POST "/ore/save" [:as {uri :uri}]
        :query [params StandardUserQueryParams]
        :summary "Generating an OAI-ORE File for a Data Set"
        :description (str
  "Generates an OAI-ORE file serialized as RDF/XML for the data set and saves it in a separate
  folder in the data store. The user must have write access to the data set folder, and must be able
  to create the related metadata folder. The metadata folder is determined using a relative path. For
  example, if the data set is stored in /zone/home/user/repo/curated/dataset then the metadata files
  might be stored in /zone/home/user/repo/curated_metadata/dataset. The curated_metadata folder name
  is configurable, so the path to the metadata folder might be slightly different."
  (get-error-code-block
   "ERR_NOT_A_USER, ERR_DOES_NOT_EXIST, ERR_NOT_A_FOLDER, ERR_NOT_WRITEABLE, ERR_BAD_PATH_LENGTH")
  (get-endpoint-delegate-block
   "metadata"
   "GET /avus/{target-type}/{target-id}"))
        (svc/trap uri meta/do-ore-save data-id params)))))
