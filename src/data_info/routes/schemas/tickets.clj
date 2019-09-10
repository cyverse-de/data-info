(ns data-info.routes.schemas.tickets
  (:use [common-swagger-api.schema :only [StandardUserQueryParams]])
  (:require [common-swagger-api.schema.data.tickets :as schema]
            [schema.core :as s]))

(s/defschema AddTicketQueryParams
  (merge StandardUserQueryParams
         schema/AddTicketQueryParams))

(s/defschema DeleteTicketQueryParams
  (merge StandardUserQueryParams
         schema/DeleteTicketQueryParams))
