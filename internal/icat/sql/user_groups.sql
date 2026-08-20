-- The names of the groups a user belongs to.
--
-- The reference asks iRODS over the protocol. Asking the catalog instead removes the last
-- protocol round trip from the read path, which matters because a zone may grant this
-- service very few concurrent connections -- a read that needed one would fail whenever
-- request traffic already held it.
--
-- r_user_group maps a member to the groups containing them, and a group is itself a row in
-- r_user_main. iRODS also records every user as a group of one containing themselves; the
-- reference does not report that, so it is excluded here. Verified against the running
-- service, which lists only the real groups.
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
