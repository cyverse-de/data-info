-- Whether an account exists in the zone, and what kind it is.
--
-- Ported from clj-icat-direct's mk-user. The reference asks iRODS this over the protocol,
-- but the catalog is where iRODS keeps the answer, and every request validates its caller --
-- so asking here costs a single indexed row rather than a protocol connection per request,
-- on a zone that grants this service very few.
--
-- $1 username, $2 zone
SELECT u.user_type_name
  FROM r_user_main u
 WHERE u.user_name = $1
   AND u.zone_name = $2
