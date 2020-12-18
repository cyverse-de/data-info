(ns data-info.routes.exists
  (:use [common-swagger-api.schema]
        [otel.middleware :only [otel-middleware]]
        [ring.util.http-response :only [ok]])
  (:require [common-swagger-api.schema.data.exists :as schema]
            [data-info.services.exists :as exists]))


(defroutes existence-marker

  (context "/existence-marker" []
    :tags ["bulk"]

    (POST "/" []
      :middleware [otel-middleware]
      :query [params StandardUserQueryParams]
      :body [body schema/ExistenceRequest]
      :responses schema/ExistenceResponses
      :summary schema/ExistenceSummary
      :description schema/ExistenceDocs
      (ok (exists/do-exists params body)))))
