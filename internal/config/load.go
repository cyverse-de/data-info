package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/cyverse-de/go-mod/cfg"
	"github.com/knadh/koanf"
	"github.com/mitchellh/mapstructure"
)

// Settings selects where configuration is read from. The zero value uses go-mod/cfg's
// DE-wide defaults.
type Settings struct {
	ConfigPath string
	DotEnvPath string
	EnvPrefix  string
}

// DefaultEnvPrefix is the environment prefix for this service, so DISCOENV_IRODS_PASSWORD
// sets irods.password.
const DefaultEnvPrefix = "DISCOENV_"

// Load reads the configuration, applies defaults, and validates the result. It returns an
// error describing every problem it found rather than only the first, because a
// misconfigured deployment usually has more than one.
func Load(s Settings) (*Config, error) {
	if s.EnvPrefix == "" {
		s.EnvPrefix = DefaultEnvPrefix
	}

	k, err := cfg.Init(&cfg.Settings{
		EnvPrefix:  s.EnvPrefix,
		ConfigPath: s.ConfigPath,
		DotEnvPath: s.DotEnvPath,
		FileType:   cfg.YAML,
	})
	if err != nil {
		return nil, fmt.Errorf("reading configuration: %w", err)
	}

	return fromKoanf(k)
}

func fromKoanf(k *koanf.Koanf) (*Config, error) {
	c := defaults()

	// ZeroFields matters here. mapstructure's default is to merge a decoded slice into
	// the existing one element by element, so a one-element permsfilter decoded over the
	// two-element default would yield the configured value followed by a leftover
	// "rodsadmin" -- a configured list silently gaining entries nobody asked for. With
	// ZeroFields the slice and map fields are replaced outright. Scalars are unaffected:
	// mapstructure only touches fields the input actually contains, so the defaults set
	// above still survive for anything the deployment leaves out.
	//
	// The hooks are koanf's own defaults, restated because supplying a DecoderConfig
	// replaces them wholesale.
	if err := k.UnmarshalWithConf("", c, koanf.UnmarshalConf{
		DecoderConfig: &mapstructure.DecoderConfig{
			DecodeHook: mapstructure.ComposeDecodeHookFunc(
				mapstructure.StringToTimeDurationHookFunc(),
				mapstructure.StringToSliceHookFunc(","),
				mapstructure.TextUnmarshallerHookFunc()),
			Result:           c,
			WeaklyTypedInput: true,
			ZeroFields:       true,
		},
	}); err != nil {
		return nil, fmt.Errorf("parsing configuration: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// defaults returns a Config pre-populated with every default, so that unmarshalling only
// has to overwrite what the deployment actually sets.
func defaults() *Config {
	c := &Config{
		Port:              DefaultPort,
		PermsFilter:       DefaultPermsFilter(),
		CommunityData:     DefaultCommunityData,
		BadChars:          DefaultBadChars,
		MaxPathsInRequest: DefaultMaxPathsInRequest,
		AnonUser:          DefaultAnonUser,
	}

	c.Timeouts.Request = DefaultRequestTimeout
	c.Timeouts.Upload = DefaultUploadTimeout

	c.Kifshare.DownloadTemplate = DefaultKifshareDownloadTemplate

	c.Services.AsyncTasks = DefaultAsyncTasksBaseURL
	c.Services.Metadata = DefaultMetadataBaseURL
	c.Services.NotificationAgent = DefaultNotificationAgentBaseURL

	c.IRODS.Host = DefaultIRODSHost
	c.IRODS.Port = DefaultIRODSPort
	c.IRODS.Zone = DefaultIRODSZone
	c.IRODS.User = DefaultIRODSUser
	c.IRODS.Home = DefaultIRODSHome
	c.IRODS.MaxRetries = DefaultIRODSMaxRetries
	c.IRODS.RetrySleep = DefaultIRODSRetrySleep
	c.IRODS.UseTrash = true
	c.IRODS.AdminUsers = DefaultIRODSAdminUsers()

	c.ICAT.Host = DefaultICATHost
	c.ICAT.Port = DefaultICATPort
	c.ICAT.User = DefaultICATUser
	c.ICAT.Database = DefaultICATDatabase
	c.ICAT.SSLMode = DefaultICATSSLMode

	c.TypeDetect.TypeAttribute = DefaultTypeAttribute

	c.PathLists.HT.FileIdentifier = DefaultHTPathListFileIdentifier
	c.PathLists.HT.InfoType = DefaultHTPathListInfoType
	c.PathLists.MultiInput.FileIdentifier = DefaultMultiInputPathListFileIdentifier
	c.PathLists.MultiInput.InfoType = DefaultMultiInputPathListInfoType

	c.AMQP.URI = DefaultAMQPURI
	c.AMQP.Exchange.Name = DefaultAMQPExchangeName
	c.AMQP.Exchange.Durable = true
	c.AMQP.Exchange.AutoDelete = false

	c.DataONE.MemberNodeBase = DefaultDataONEMemberNodeBase
	c.DataONE.OREAttribute = DefaultOREAttribute
	c.DataONE.FormatIDAttribute = DefaultD1FormatIDAttribute
	c.DataONE.MetadataDirname = DefaultD1MetadataDirname
	c.DataONE.MetadataDirpathAttribute = DefaultD1MetadataDirpathAttr

	return c
}

// Validate reports every problem with the configuration at once. A service that starts
// with bad configuration only discovers it mid-operation, so this runs before anything
// opens a connection.
func (c *Config) Validate() error {
	var problems []error

	add := func(format string, args ...any) {
		problems = append(problems, fmt.Errorf(format, args...))
	}

	if c.Port < 1 || c.Port > 65535 {
		add("port must be between 1 and 65535, got %d", c.Port)
	}
	if c.Timeouts.Request <= 0 {
		add("timeouts.request must be positive, got %s", c.Timeouts.Request)
	}
	if c.Timeouts.Upload <= 0 {
		add("timeouts.upload must be positive, got %s", c.Timeouts.Upload)
	}
	if c.Timeouts.Upload > 0 && c.Timeouts.Request > 0 && c.Timeouts.Upload < c.Timeouts.Request {
		// The split exists so file transfers get the longer budget. Inverting it makes
		// uploads stricter than ordinary requests, which is never what anyone means.
		add("timeouts.upload (%s) must be at least timeouts.request (%s)",
			c.Timeouts.Upload, c.Timeouts.Request)
	}
	if c.MaxPathsInRequest < 1 {
		add("maxpaths must be at least 1, got %d", c.MaxPathsInRequest)
	}
	if !strings.HasPrefix(c.CommunityData, "/") {
		add("communitydata must be an absolute iRODS path, got %q", c.CommunityData)
	}
	if c.AnonUser == "" {
		add("anonuser must not be empty")
	}

	// The three the Clojure service declared with defprop-str, i.e. required.
	requireURL(add, "anonfiles.baseurl", c.AnonFiles.BaseURL)
	requireURL(add, "kifshare.externalurl", c.Kifshare.ExternalURL)
	if len(c.AnonFiles.Mappings) == 0 {
		add("anonfiles.mappings must contain at least one path prefix mapping")
	}
	for prefix := range c.AnonFiles.Mappings {
		if !strings.HasPrefix(prefix, "/") {
			add("anonfiles.mappings key %q must be an absolute iRODS path", prefix)
		}
	}
	if c.Kifshare.DownloadTemplate == "" {
		add("kifshare.downloadtemplate must not be empty")
	}

	requireURL(add, "services.asynctasks", c.Services.AsyncTasks)
	requireURL(add, "services.metadata", c.Services.Metadata)
	requireURL(add, "services.notificationagent", c.Services.NotificationAgent)

	if c.IRODS.Host == "" {
		add("irods.host must not be empty")
	}
	if c.IRODS.Port < 1 || c.IRODS.Port > 65535 {
		add("irods.port must be between 1 and 65535, got %d", c.IRODS.Port)
	}
	if c.IRODS.Zone == "" {
		add("irods.zone must not be empty")
	}
	if c.IRODS.User == "" {
		add("irods.user must not be empty")
	}
	if c.IRODS.Password == "" {
		add("irods.password must not be empty (set DISCOENV_IRODS_PASSWORD)")
	}
	if !strings.HasPrefix(c.IRODS.Home, "/") {
		add("irods.home must be an absolute iRODS path, got %q", c.IRODS.Home)
	}
	if c.IRODS.MaxRetries < 0 {
		add("irods.maxretries must not be negative, got %d", c.IRODS.MaxRetries)
	}
	if c.IRODS.RetrySleep < 0 {
		add("irods.retrysleep must not be negative, got %s", c.IRODS.RetrySleep)
	}

	if c.ICAT.Host == "" {
		add("icat.host must not be empty")
	}
	if c.ICAT.Port < 1 || c.ICAT.Port > 65535 {
		add("icat.port must be between 1 and 65535, got %d", c.ICAT.Port)
	}
	if c.ICAT.User == "" {
		add("icat.user must not be empty")
	}
	if c.ICAT.Password == "" {
		add("icat.password must not be empty (set DISCOENV_ICAT_PASSWORD)")
	}
	if c.ICAT.Database == "" {
		add("icat.database must not be empty")
	}
	switch c.ICAT.SSLMode {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
	default:
		add("icat.sslmode %q is not a libpq sslmode", c.ICAT.SSLMode)
	}

	if c.TypeDetect.TypeAttribute == "" {
		add("typedetect.attribute must not be empty")
	}

	if c.AMQP.URI == "" {
		add("amqp.uri must not be empty")
	} else if u, err := url.Parse(c.AMQP.URI); err != nil {
		add("amqp.uri is not a valid URL: %v", err)
	} else if u.Scheme != "amqp" && u.Scheme != "amqps" {
		add("amqp.uri must use the amqp or amqps scheme, got %q", u.Scheme)
	}
	if c.AMQP.Exchange.Name == "" {
		add("amqp.exchange.name must not be empty")
	}

	requireURL(add, "dataone.membernodebase", c.DataONE.MemberNodeBase)

	return errors.Join(problems...)
}

// requireURL reports a missing or malformed http(s) base URL.
func requireURL(add func(string, ...any), name, raw string) {
	if raw == "" {
		add("%s is required", name)
		return
	}
	u, err := url.Parse(raw)
	if err != nil {
		add("%s is not a valid URL: %v", name, err)
		return
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		add("%s must use the http or https scheme, got %q", name, u.Scheme)
	}
	if u.Host == "" {
		add("%s must include a host, got %q", name, raw)
	}
}

// ICATConnectionString renders the ICAT connection as a libpq URI.
func (c *Config) ICATConnectionString() string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.ICAT.User, c.ICAT.Password),
		Host:   fmt.Sprintf("%s:%d", c.ICAT.Host, c.ICAT.Port),
		Path:   "/" + c.ICAT.Database,
	}
	q := u.Query()
	q.Set("sslmode", c.ICAT.SSLMode)
	u.RawQuery = q.Encode()
	return u.String()
}

// UploadTimeout and RequestTimeout exist so handlers need not reach into the struct.
func (c *Config) UploadTimeout() time.Duration  { return c.Timeouts.Upload }
func (c *Config) RequestTimeout() time.Duration { return c.Timeouts.Request }
