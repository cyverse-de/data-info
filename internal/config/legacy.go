package config

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// maskedValue is what clojure-commons' mask-prop substitutes for a masked setting.
const maskedValue = "********"

// maskFilters reproduce clojure-commons' masking. data-info passes one filter of its own,
// and mask-config always appends #"password" and #"pass", so any key containing those is
// masked too. A filter matches anywhere in the key, not just at the start.
//
// Note what this does NOT cover: amqp.uri embeds the broker password in its userinfo, and
// the key "data-info.amqp.uri" matches none of these patterns, so the credential is
// returned in the clear. That is today's behaviour and is reproduced deliberately; see
// docs/deferred-fixes.md entry 5.
var maskFilters = []*regexp.Regexp{
	regexp.MustCompile(`(?:irods|icat)[-.](?:user|pass)`),
	regexp.MustCompile(`password`),
	regexp.MustCompile(`pass`),
}

// LegacyMap renders the configuration under the flat data-info.* property names the
// Clojure service used, with the same masking, for GET /admin/config.
//
// Every value is a string. The Clojure service read a Java properties file, so even
// numbers and booleans came back quoted, and callers diffing the two services' output
// depend on that.
//
// Two deliberate differences from the Clojure output. It returned the entire loaded
// properties file, so any key present in the deployment but unused by the code -- such as
// the orphaned data-info.copy-key -- appeared as well; this returns only settings the
// service actually has. And key order is unspecified in both, since neither serializes a
// sorted map.
func (c *Config) LegacyMap() map[string]string {
	m := map[string]string{
		"data-info.port":                   itoa(c.Port),
		"data-info.jetty.max-idle-time":    millis(c.Timeouts.Request),
		"data-info.jetty.upload-idle-time": millis(c.Timeouts.Upload),

		"data-info.perms-filter":          strings.Join(c.PermsFilter, ","),
		"data-info.community-data":        c.CommunityData,
		"data-info.bad-chars":             c.BadChars,
		"data-info.max-paths-in-request":  itoa(c.MaxPathsInRequest),
		"data-info.anon-user":             c.AnonUser,
		"data-info.anon-files-base-url":   c.AnonFiles.BaseURL,
		"data-info.anon-files-mappings":   marshalMappings(c.AnonFiles.Mappings),
		"data-info.kifshare-external-url": c.Kifshare.ExternalURL,

		"data-info.kifshare-download-template": c.Kifshare.DownloadTemplate,
		"data-info.async-tasks.base-url":       c.Services.AsyncTasks,
		"data-info.metadata.base-url":          c.Services.Metadata,
		"data-info.notificationagent.base-url": c.Services.NotificationAgent,

		"data-info.irods.host":        c.IRODS.Host,
		"data-info.irods.port":        itoa(c.IRODS.Port),
		"data-info.irods.zone":        c.IRODS.Zone,
		"data-info.irods.user":        c.IRODS.User,
		"data-info.irods.password":    c.IRODS.Password,
		"data-info.irods.home":        c.IRODS.Home,
		"data-info.irods.resc":        c.IRODS.Resource,
		"data-info.irods.max-retries": itoa(c.IRODS.MaxRetries),
		"data-info.irods.retry-sleep": millis(c.IRODS.RetrySleep),
		"data-info.irods.use-trash":   strconv.FormatBool(c.IRODS.UseTrash),
		"data-info.irods.admin-users": strings.Join(c.IRODS.AdminUsers, ","),

		"data-info.icat.host":     c.ICAT.Host,
		"data-info.icat.port":     itoa(c.ICAT.Port),
		"data-info.icat.user":     c.ICAT.User,
		"data-info.icat.password": c.ICAT.Password,
		"data-info.icat.db":       c.ICAT.Database,

		"data-info.type-detect.type-attribute": c.TypeDetect.TypeAttribute,

		"data-info.path-list.ht.file-identifier":          c.PathLists.HT.FileIdentifier,
		"data-info.path-list.ht.info-type":                c.PathLists.HT.InfoType,
		"data-info.path-list.multi-input.file-identifier": c.PathLists.MultiInput.FileIdentifier,
		"data-info.path-list.multi-input.info-type":       c.PathLists.MultiInput.InfoType,

		"data-info.amqp.uri":                  c.AMQP.URI,
		"data-info.amqp.exchange.name":        c.AMQP.Exchange.Name,
		"data-info.amqp.exchange.durable":     strconv.FormatBool(c.AMQP.Exchange.Durable),
		"data-info.amqp.exchange.auto-delete": strconv.FormatBool(c.AMQP.Exchange.AutoDelete),

		"data-info.dataone-member-node.base":      c.DataONE.MemberNodeBase,
		"data-info.ore-attr":                      c.DataONE.OREAttribute,
		"data-info.d1-format-id-attr":             c.DataONE.FormatIDAttribute,
		"data-info.d1-metadata-base":              c.DataONE.MetadataDirname,
		"data-info.d1-metadata-dirpath-attribute": c.DataONE.MetadataDirpathAttribute,
	}

	for k := range m {
		if masked(k) {
			m[k] = maskedValue
		}
	}
	return m
}

// masked reports whether a property's value should be replaced before it is returned.
func masked(key string) bool {
	for _, f := range maskFilters {
		if f.MatchString(key) {
			return true
		}
	}
	return false
}

func itoa(n int) string { return strconv.Itoa(n) }

// millis renders a duration the way the Clojure Jetty settings expressed one.
func millis(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) }

// marshalMappings renders the anon-files mappings as the JSON object the Clojure service
// carried inside a single properties value.
func marshalMappings(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}
