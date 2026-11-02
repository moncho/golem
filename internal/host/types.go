package host

// Thread names a conversation the way the application host does.
type Thread struct {
	ID      string `json:"id"`
	Source  string `json:"source"`
	Channel string `json:"channel"`
	ReplyTo string `json:"replyTo"`
}

// Input is one message offered to the application for admission.
type Input struct {
	Key      string `json:"key"`
	Thread   Thread `json:"thread"`
	Text     string `json:"text"`
	Sender   string `json:"sender"`
	Explicit bool   `json:"explicit"`
	Bot      bool   `json:"bot,omitempty"`
}

// Model selects the provider models; APIKey is passed to the host once, at init.
type Model struct {
	Provider      string `json:"provider"`
	ModelID       string `json:"modelId"`
	RouterModelID string `json:"routerModelId,omitempty"`
	BaseURL       string `json:"baseUrl,omitempty"`
	APIKey        string `json:"apiKey,omitempty"`
}

// Init is the first message to the host. Zero tuning fields keep the host's
// defaults.
type Init struct {
	State        string   `json:"state"`
	Model        Model    `json:"model"`
	Proactive    string   `json:"proactive"`
	Channels     []string `json:"channels"`
	Users        []string `json:"users"`
	Bots         []string `json:"bots"`
	Environments []string `json:"environments"`
	Principles   bool     `json:"principles"`
	Memory       bool     `json:"memory"`

	MaxWorkers       int `json:"maxWorkers,omitempty"`
	PollMs           int `json:"pollMs,omitempty"`
	WatchMs          int `json:"watchMs,omitempty"`
	AuditMs          int `json:"auditMs,omitempty"`
	DeliveryAttempts int `json:"deliveryAttempts,omitempty"`
	RetainMs         int `json:"retainMs,omitempty"`
	MaxEventKeys     int `json:"maxEventKeys,omitempty"`
	MaxArchive       int `json:"maxArchive,omitempty"`
}
