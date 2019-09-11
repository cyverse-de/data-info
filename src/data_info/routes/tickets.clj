(ns data-info.routes.tickets
  (:use [common-swagger-api.schema]
        [data-info.routes.schemas.tickets]
        [ring.util.http-response :only [ok]])
  (:require [common-swagger-api.schema.data :as data-schema]
            [common-swagger-api.schema.data.tickets :as schema]
            [data-info.services.tickets :as tickets]))


(defroutes ticket-routes

  (context "/tickets" []
    :tags ["tickets"]

    (POST "/" []
      :query [params AddTicketQueryParams]
      :body [body data-schema/Paths]
      :responses schema/AddTicketResponses
      :summary schema/AddTicketSummary
      :description schema/AddTicketDocs
      (ok (tickets/do-add-tickets params body))))

  (POST "/ticket-lister" []
    :tags ["tickets"]
    :query [params StandardUserQueryParams]
    :body [body data-schema/Paths]
    :responses schema/ListTicketResponses
    :summary schema/ListTicketSummary
    :description schema/ListTicketDocs
    (ok (tickets/do-list-tickets params body)))

  (POST "/ticket-deleter" []
    :tags ["tickets"]
    :query [params DeleteTicketQueryParams]
    :body [body schema/Tickets]
    :responses schema/DeleteTicketResponses
    :summary schema/DeleteTicketSummary
    :description schema/DeleteTicketDocs
    (ok (tickets/do-remove-tickets params body))))
