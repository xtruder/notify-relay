package protocol

type Hint struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

type NotifyRequest struct {
	AppName       string   `json:"app_name"`
	ReplacesID    uint32   `json:"replaces_id"`
	AppIcon       string   `json:"app_icon"`
	Summary       string   `json:"summary"`
	Body          string   `json:"body"`
	Actions       []string `json:"actions"`
	Hints         []Hint   `json:"hints"`
	ExpireTimeout int32    `json:"expire_timeout"`
	Wait          bool     `json:"wait"`
	PrintID       bool     `json:"print_id"`
}

type NotifyResponse struct {
	ID        uint32 `json:"id"`
	Event     string `json:"event,omitempty"`
	Reason    uint32 `json:"reason,omitempty"`
	ActionKey string `json:"action_key,omitempty"`
}

type CloseRequest struct {
	ID uint32 `json:"id"`
}

type CapabilitiesResponse struct {
	Capabilities []string `json:"capabilities"`
}

type ServerInfoResponse struct {
	Name    string `json:"name"`
	Vendor  string `json:"vendor"`
	Version string `json:"version"`
	Spec    string `json:"spec"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}
