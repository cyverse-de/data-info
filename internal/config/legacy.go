package config

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// maskedValue replaces a masked setting's value.
const maskedValue = "********"

// maskFilters reproduce the legacy clojure-commons masking: data-info's own filter plus
// "password" and "pass", each matched anywhere in the key. They miss amqp.uri, whose password
// is in its userinfo, so maskURIPassword handles that one.
var maskFilters = []*regexp.Regexp{
	regexp.MustCompile(`(?:irods|icat)[-.](?:user|pass)`),
	regexp.MustCompile(`password`),
	regexp.MustCompile(`pass`),
}

// LegacyMap renders the configuration under the legacy flat data-info.* property names,
// every value a string and secrets masked, for GET /admin/config.
func (c *Config) LegacyMap() map[string]string {
	m := map[string]string{
		"data-info.port":                   strconv.Itoa(c.Port),
		"data-info.jetty.max-idle-time":    millis(c.Timeouts.Request),
		"data-info.jetty.upload-idle-time": millis(c.Timeouts.Upload),

		"data-info.perms-filter":          strings.Join(c.PermsFilter, ","),
		"data-info.community-data":        c.CommunityData,
		"data-info.bad-chars":             c.BadChars,
		"data-info.max-paths-in-request":  strconv.Itoa(c.MaxPathsInRequest),
		"data-info.anon-user":             c.AnonUser,
		"data-info.anon-files-base-url":   c.AnonFiles.BaseURL,
		"data-info.anon-files-mappings":   marshalMappings(c.AnonFiles.Mappings),
		"data-info.kifshare-external-url": c.Kifshare.ExternalURL,

		"data-info.kifshare-download-template": c.Kifshare.DownloadTemplate,
		"data-info.async-tasks.base-url":       c.Services.AsyncTasks,
		"data-info.metadata.base-url":          c.Services.Metadata,
		"data-info.notificationagent.base-url": c.Services.NotificationAgent,

		"data-info.irods.host":        c.IRODS.Host,
		"data-info.irods.port":        strconv.Itoa(c.IRODS.Port),
		"data-info.irods.zone":        c.IRODS.Zone,
		"data-info.irods.user":        c.IRODS.User,
		"data-info.irods.password":    c.IRODS.Password,
		"data-info.irods.home":        c.IRODS.Home,
		"data-info.irods.resc":        c.IRODS.Resource,
		"data-info.irods.max-retries": strconv.Itoa(c.IRODS.MaxRetries),
		"data-info.irods.retry-sleep": millis(c.IRODS.RetrySleep),
		"data-info.irods.use-trash":   strconv.FormatBool(c.IRODS.UseTrash),
		"data-info.irods.admin-users": strings.Join(c.IRODS.AdminUsers, ","),

		"data-info.icat.host":     c.ICAT.Host,
		"data-info.icat.port":     strconv.Itoa(c.ICAT.Port),
		"data-info.icat.user":     c.ICAT.User,
		"data-info.icat.password": c.ICAT.Password,
		"data-info.icat.db":       c.ICAT.Database,

		"data-info.type-detect.type-attribute": c.TypeDetect.TypeAttribute,

		"data-info.path-list.ht.file-identifier":          c.PathLists.HT.FileIdentifier,
		"data-info.path-list.ht.info-type":                c.PathLists.HT.InfoType,
		"data-info.path-list.multi-input.file-identifier": c.PathLists.MultiInput.FileIdentifier,
		"data-info.path-list.multi-input.info-type":       c.PathLists.MultiInput.InfoType,

		"data-info.amqp.uri":                  maskURIPassword(c.AMQP.URI),
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

// maskURIPassword hides the password in a URI's userinfo.
func maskURIPassword(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return maskedValue
	}
	return u.Redacted()
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

// millis renders a duration in whole milliseconds, the unit of the legacy jetty.* keys.
func millis(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) }

// marshalMappings renders the anon-files mappings as one JSON string, the legacy property's
// shape.
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
