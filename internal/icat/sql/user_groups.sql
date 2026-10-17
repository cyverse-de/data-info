-- Read from the catalog so the read path needs no protocol connection, which a busy zone may
-- not have free.
--
-- iRODS records each user as a group of one containing themselves. The Clojure service's
-- group listing never included that self-group, so it is excluded.
--
-- $1 username, $2 zone
SELECT g.user_name
  FROM r_user_group ug
  JOIN r_user_main u ON u.user_id = ug.user_id
  JOIN r_user_main g ON g.user_id = ug.group_user_id
 WHERE u.user_name = $1
   AND u.zone_name = $2
   AND g.user_id != u.user_id
 ORDER BY g.user_name
