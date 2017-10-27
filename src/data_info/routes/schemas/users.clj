(ns data-info.routes.schemas.users
  (:use [common-swagger-api.schema :only [describe
                                          NonBlankString
                                          StandardUserQueryParams]]
        [data-info.routes.schemas.common])
  (:require [schema.core :as s]))

(s/defschema QualifiedUser (describe NonBlankString "A qualified username like foo#bar"))
(s/defschema UserGroupsReturn
  {:user (describe QualifiedUser "The user as requested, but qualified")
   :groups (describe [QualifiedUser] "The list of qualified group names of groups this user belongs to")})
