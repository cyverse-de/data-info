(ns data-info.routes.tickets
  (:use [common-swagger-api.schema]
        [data-info.routes.schemas.common :only [get-error-code-block]]
        [data-info.routes.schemas.tickets])
  (:require [common-swagger-api.schema.data :as data-schema]
            [common-swagger-api.schema.data.tickets :as schema]
            [data-info.services.tickets :as tickets]
            [data-info.util.service :as svc]))


(defroutes ticket-routes

  (context "/tickets" []
    :tags ["tickets"]

    (POST "/" [:as {uri :uri}]
      :query [params AddTicketQueryParams]
      :body [body data-schema/Paths]
      :return schema/AddTicketResponse
      :summary "Create tickets"
      :description (str
"This endpoint allows creating tickets for a set of provided paths"
(get-error-code-block "ERR_NOT_A_USER, ERR_DOES_NOT_EXIST, ERR_NOT_WRITEABLE, ERR_TOO_MANY_RESULTS"))
      (svc/trap uri tickets/do-add-tickets params body)))

  (POST "/ticket-lister" [:as {uri :uri}]
    :tags ["tickets"]
    :query [params StandardUserQueryParams]
    :body [body data-schema/Paths]
    :return (doc-only schema/ListTicketsResponse schema/ListTicketsDocumentation)
    :summary "List tickets"
    :description (str
"This endpoint lists tickets for a set of provided paths."
(get-error-code-block "ERR_NOT_A_USER, ERR_DOES_NOT_EXIST, ERR_NOT_READABLE, ERR_TOO_MANY_RESULTS"))
    (svc/trap uri tickets/do-list-tickets params body))

  (POST "/ticket-deleter" [:as {uri :uri}]
    :tags ["tickets"]
    :query [params DeleteTicketQueryParams]
    :body [body schema/Tickets]
    :return schema/DeleteTicketsResponse
    :summary "Delete tickets"
    :description (str
"This endpoint deletes the provided set of tickets."
(get-error-code-block "ERR_NOT_A_USER, ERR_TICKET_DOES_NOT_EXIST, ERR_NOT_WRITEABLE"))
    (svc/trap uri tickets/do-remove-tickets params body)))
