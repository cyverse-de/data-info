// Package config holds data-info's runtime configuration.
//
// Values are loaded with go-mod/cfg (koanf), whose precedence is
// yaml < dotenv < environment. Secrets therefore have an environment fallback and need
// never appear on the command line.
//
// The keys here are the modern nested YAML names. The Clojure service read a flat
// data-info.* properties file, and GET /admin/config still reports those legacy names --
// see legacy.go for why.
//
// Every key segment is a single lowercase word, matching the newest DE services
// (amqp.queueprefix, datausage.refreshinterval). That is not a style preference: go-mod/cfg
// builds its environment keys by replacing every underscore with the delimiter, so a
// segment containing an underscore cannot be set from the environment at all. Keeping the
// segments single-word means every setting -- notably irods.password, icat.password and
// amqp.uri -- has a DISCOENV_ override and no secret needs to be written to a file.
package config

import (
	"time"
)

// Default values, matching the Clojure service's defprop-opt* declarations. A constant
// exists only where the Clojure code had a default; the three properties it declared with
// defprop-str are required here too.
const (
	DefaultPort              = 60000
	DefaultRequestTimeout    = 200 * time.Second
	DefaultUploadTimeout     = 3600 * time.Second
	DefaultCommunityData     = "/iplant/home/shared"
	DefaultMaxPathsInRequest = 1000
	DefaultAnonUser          = "anonymous"

	// DefaultBadChars is U+0060 GRAVE ACCENT, U+0027 APOSTROPHE, U+000A LINE FEED and
	// U+0009 CHARACTER TABULATION -- the characters rejected in new file and folder names.
	// Spelled with escapes so the control characters are visible in review.
	DefaultBadChars = "\u0060\u0027\u000A\u0009"

	DefaultKifshareDownloadTemplate = "{{url}}/d/{{ticket-id}}/{{filename}}"

	DefaultAsyncTasksBaseURL        = "http://async-tasks:60000"
	DefaultMetadataBaseURL          = "http://metadata:60000"
	DefaultNotificationAgentBaseURL = "http://notification-agent:60000"

	DefaultIRODSHost       = "irods"
	DefaultIRODSPort       = 1247
	DefaultIRODSZone       = "iplant"
	DefaultIRODSUser       = "rods"
	DefaultIRODSHome       = "/iplant/home"
	DefaultIRODSMaxRetries = 10
	DefaultIRODSRetrySleep = 1000 * time.Millisecond

	DefaultICATHost     = "irods"
	DefaultICATPort     = 5432
	DefaultICATUser     = "rods"
	DefaultICATDatabase = "ICAT"
	DefaultICATSSLMode  = "disable"

	DefaultTypeAttribute = "ipc-filetype"

	DefaultHTPathListFileIdentifier         = "# application/vnd.de.path-list+csv; version=1"
	DefaultHTPathListInfoType               = "ht-analysis-path-list"
	DefaultMultiInputPathListFileIdentifier = "# application/vnd.de.multi-input-path-list+csv; version=1"
	DefaultMultiInputPathListInfoType       = "multi-input-path-list"

	DefaultAMQPURI          = "amqp://guest:guest@rabbit:5672/"
	DefaultAMQPExchangeName = "de"

	DefaultDataONEMemberNodeBase = "https://de.cyverse.org/dataone-node/rest/mn"
	DefaultOREAttribute          = "ipc-oai-ore"
	DefaultD1FormatIDAttribute   = "ipc-d1-format-id"
	DefaultD1MetadataDirname     = "curated_metadata"
	DefaultD1MetadataDirpathAttr = "ipc-d1-dirpath"
)

// DefaultPermsFilter and DefaultIRODSAdminUsers are the list-valued defaults. They are
// functions rather than package-level slices so a caller cannot mutate the default.
func DefaultPermsFilter() []string     { return []string{"rods", "rodsadmin"} }
func DefaultIRODSAdminUsers() []string { return []string{"rods", "rodsadmin"} }

// Config is the fully resolved configuration. Load returns one; nothing else should
// construct it except tests.
type Config struct {
	Port int `koanf:"port"`

	// Timeouts replaces the Clojure service's Jetty idle-timeout customizer. Upload
	// applies only to POST /data and PUT /data/{data-id}, which stream whole files.
	Timeouts Timeouts `koanf:"timeouts"`

	// PermsFilter names accounts stripped from permission listings and share counts.
	PermsFilter []string `koanf:"permsfilter"`

	// CommunityData is the community-data root, labelled "Community Data" in listings
	// and protected from deletion as a base path.
	CommunityData string `koanf:"communitydata"`

	// BadChars are rejected in new file and folder names, and are the default
	// bad-chars for listings.
	BadChars string `koanf:"badchars"`

	// MaxPathsInRequest bounds bulk requests; exceeding it is ERR_TOO_MANY_RESULTS.
	MaxPathsInRequest int `koanf:"maxpaths"`

	// AnonUser is the iRODS account POST /anonymizer grants read to.
	AnonUser string `koanf:"anonuser"`

	AnonFiles  AnonFiles  `koanf:"anonfiles"`
	Kifshare   Kifshare   `koanf:"kifshare"`
	Services   Services   `koanf:"services"`
	IRODS      IRODS      `koanf:"irods"`
	ICAT       ICAT       `koanf:"icat"`
	TypeDetect TypeDetect `koanf:"typedetect"`
	PathLists  PathLists  `koanf:"pathlists"`
	AMQP       AMQP       `koanf:"amqp"`
	DataONE    DataONE    `koanf:"dataone"`
}

// Timeouts bounds how long a request may stay idle.
type Timeouts struct {
	Request time.Duration `koanf:"request"`
	Upload  time.Duration `koanf:"upload"`
}

// AnonFiles configures the URLs handed back by POST /anonymizer.
type AnonFiles struct {
	// BaseURL is required.
	BaseURL string `koanf:"baseurl"`

	// Mappings maps an iRODS path prefix onto an anon-files URL fragment; the longest
	// matching prefix wins. Required, and required to be non-empty: an empty map would
	// silently produce unusable URLs rather than failing.
	//
	// The Clojure service took this as a JSON string inside a properties value. Here it
	// is a real nested mapping, so it is readable in YAML and validated on load.
	Mappings map[string]string `koanf:"mappings"`
}

// Kifshare configures ticket download URLs.
type Kifshare struct {
	// ExternalURL is required. It is both the download-page base and the {{url}} the
	// download template renders.
	ExternalURL string `koanf:"externalurl"`

	// DownloadTemplate is a mustache template over url, ticket-id and filename.
	DownloadTemplate string `koanf:"downloadtemplate"`
}

// Services holds the base URLs of the DE services data-info calls.
type Services struct {
	AsyncTasks        string `koanf:"asynctasks"`
	Metadata          string `koanf:"metadata"`
	NotificationAgent string `koanf:"notificationagent"`
}

// IRODS configures the iRODS protocol connection.
type IRODS struct {
	Host string `koanf:"host"`
	Port int    `koanf:"port"`
	Zone string `koanf:"zone"`

	// User and Password are the proxy account data-info connects as. Per-request work
	// runs in client-user mode on top of it.
	User     string `koanf:"user"`
	Password string `koanf:"password"`

	// Home is the iRODS home base, which doubles as the "Shared With Me" root.
	Home string `koanf:"home"`

	// Resource is the default iRODS resource. Empty means the server's default.
	Resource string `koanf:"resource"`

	MaxRetries int           `koanf:"maxretries"`
	RetrySleep time.Duration `koanf:"retrysleep"`

	// UseTrash controls whether deletes move to trash rather than removing outright.
	UseTrash bool `koanf:"usetrash"`

	// AdminUsers are exempt from inherit-bit removal during unsharing.
	AdminUsers []string `koanf:"adminusers"`
}

// ICAT configures the direct PostgreSQL connection to the iRODS catalog.
type ICAT struct {
	Host     string `koanf:"host"`
	Port     int    `koanf:"port"`
	User     string `koanf:"user"`
	Password string `koanf:"password"`
	Database string `koanf:"database"`

	// SSLMode is libpq's sslmode. It defaults to disable, which is what the DE deploys
	// today, but it is a setting rather than a constant so a deployment can require TLS
	// to the catalog without a code change.
	SSLMode string `koanf:"sslmode"`
}

// TypeDetect names the AVU attribute holding a data object's info type. Detection itself
// belongs to the info-typer service; data-info only reads and writes the attribute.
//
// The Clojure service also had data-info.type-detect.read-amount, the number of bytes it
// sipped from an upload to sniff the type. It is deliberately not ported: the sniffing it
// governed moved to info-typer along with heuristomancer, so carrying the setting here
// would be dead configuration. A deployment that tuned it should move the value to
// info-typer.filetype-read-amount. Expect it to show up as a missing key when
// GET /admin/config is diffed against the Clojure service.
type TypeDetect struct {
	TypeAttribute string `koanf:"attribute"`
}

// PathLists configures generated path-list files.
type PathLists struct {
	HT         PathList `koanf:"ht"`
	MultiInput PathList `koanf:"multiinput"`
}

// PathList is one path-list flavour: the header line written into the file and the info
// type tagged onto it.
type PathList struct {
	FileIdentifier string `koanf:"identifier"`
	InfoType       string `koanf:"infotype"`
}

// AMQP configures the broker data-info publishes usage-reindex messages to.
type AMQP struct {
	URI      string       `koanf:"uri"`
	Exchange AMQPExchange `koanf:"exchange"`
}

// AMQPExchange describes the exchange declared at startup.
type AMQPExchange struct {
	Name       string `koanf:"name"`
	Durable    bool   `koanf:"durable"`
	AutoDelete bool   `koanf:"autodelete"`
}

// DataONE configures OAI-ORE and DataCite generation.
type DataONE struct {
	MemberNodeBase string `koanf:"membernodebase"`

	// OREAttribute marks a file as an ORE document.
	OREAttribute string `koanf:"oreattr"`

	// FormatIDAttribute holds the ORE format id.
	FormatIDAttribute string `koanf:"formatidattr"`

	// MetadataDirname is the sibling directory generated metadata is written into.
	MetadataDirname string `koanf:"metadatadirname"`

	// MetadataDirpathAttribute points a data set at its metadata directory.
	MetadataDirpathAttribute string `koanf:"metadatadirpathattr"`
}
