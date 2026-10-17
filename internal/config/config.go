// Package config loads data-info's configuration via go-mod/cfg (yaml < dotenv < environment).
// Key segments are single lowercase words because go-mod/cfg turns every underscore in an
// environment name into a delimiter, so a segment containing one could not be set from the
// environment, where secrets belong.
package config

import (
	"time"
)

// Defaults for optional settings. Required settings have none.
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

	DefaultIRODSHost                 = "irods"
	DefaultIRODSPort                 = 1247
	DefaultIRODSZone                 = "iplant"
	DefaultIRODSUser                 = "rods"
	DefaultIRODSHome                 = "/iplant/home"
	DefaultIRODSMaxSessions          = 32
	DefaultIRODSMaxTotalConnections  = 8
	DefaultIRODSMaxConnections       = 4
	DefaultIRODSSessionIdleTimeout   = 5 * time.Minute
	DefaultIRODSOperationTimeout     = 1 * time.Minute
	DefaultIRODSLongOperationTimeout = 5 * time.Minute
	DefaultIRODSMaxRetries           = 10
	DefaultIRODSRetrySleep           = 1000 * time.Millisecond

	DefaultICATHost     = "irods"
	DefaultICATPort     = 5432
	DefaultICATUser     = "rods"
	DefaultICATDatabase = "ICAT"
	DefaultICATSSLMode  = "disable"

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

	// Timeouts.Upload applies only to POST /data and PUT /data/{data-id}, which stream whole
	// files; Timeouts.Request applies to everything else.
	Timeouts Timeouts `koanf:"timeouts"`

	// PermsFilter names accounts stripped from permission listings and share counts.
	PermsFilter []string `koanf:"permsfilter"`

	// CommunityData is the community-data root, labelled "Community Data" in listings
	// and protected from deletion as a base path.
	CommunityData string `koanf:"communitydata"`

	// BadChars are rejected in new file and folder names. Listings take theirs from the request.
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

	// AdminUsers hold structural access that sharing changes and permission repairs leave alone.
	AdminUsers []string `koanf:"adminusers"`

	// MaxSessions bounds how many distinct users' sessions are kept, and MaxConnections
	// how many metadata connections each may open.
	MaxSessions    int `koanf:"maxsessions"`
	MaxConnections int `koanf:"maxconnections"`

	// MaxTotalConnections is the ceiling on iRODS connections this replica holds open; a
	// call that would cross it waits. Size it below what the Data Store will accept from
	// one node, less what the node's other pods use.
	MaxTotalConnections int `koanf:"maxtotalconnections"`

	// SessionIdleTimeout evicts a user's connections after a period of disuse.
	SessionIdleTimeout time.Duration `koanf:"sessionidletimeout"`

	// OperationTimeout and LongOperationTimeout become socket deadlines. They bound how
	// long an abandoned call can hold a connection after its caller has given up, so
	// neither should be generous; listing and bulk calls run under the long one.
	OperationTimeout     time.Duration `koanf:"operationtimeout"`
	LongOperationTimeout time.Duration `koanf:"longoperationtimeout"`
}

// ICAT configures the direct PostgreSQL connection to the iRODS catalog.
type ICAT struct {
	Host     string `koanf:"host"`
	Port     int    `koanf:"port"`
	User     string `koanf:"user"`
	Password string `koanf:"password"`
	Database string `koanf:"database"`

	// SSLMode is libpq's sslmode.
	SSLMode string `koanf:"sslmode"`
}

// TypeDetect names the AVU attribute holding a data object's info type. Detection itself
// belongs to the info-typer service; data-info only reads and writes the attribute.
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
