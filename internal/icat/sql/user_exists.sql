-- Whether an account exists in the zone, and what kind it is.
--
-- Read from the catalog rather than over the protocol: every request validates its caller,
-- and the zone grants this service few protocol connections.
--
-- $1 username, $2 zone
SELECT u.user_type_name
  FROM r_user_main u
 WHERE u.user_name = $1
   AND u.zone_name = $2
