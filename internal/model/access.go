package model

type Credential struct {
	ID              int64  `json:"id"`
	Alias           string `json:"alias"`
	Kind            string `json:"kind"`
	ExpiresAt       string `json:"expires_at,omitempty"`
	ExpiryStatus    string `json:"expiry_status"`
	ExpirySource    string `json:"expiry_source,omitempty"`
	ExpiryCheckedAt string `json:"expiry_checked_at,omitempty"`
	Enabled         bool   `json:"enabled"`
	Priority        int    `json:"priority"`
	SecretHint      string `json:"secret_hint"`
	State           string `json:"state"`
	LastError       string `json:"last_error,omitempty"`
	LastUsedAt      string `json:"last_used_at,omitempty"`
	LastSuccessAt   string `json:"last_success_at,omitempty"`
	UpdatedAt       string `json:"updated_at"`
}

type CredentialExpiry struct {
	ExpiresAt string
	Status    string
	Source    string
	CheckedAt string
}

type Proxy struct {
	ID            int64    `json:"id"`
	Alias         string   `json:"alias"`
	Scheme        string   `json:"scheme"`
	EndpointHint  string   `json:"endpoint_hint"`
	Providers     []string `json:"providers"`
	Enabled       bool     `json:"enabled"`
	Priority      int      `json:"priority"`
	State         string   `json:"state"`
	LastError     string   `json:"last_error,omitempty"`
	LastUsedAt    string   `json:"last_used_at,omitempty"`
	LastSuccessAt string   `json:"last_success_at,omitempty"`
	UpdatedAt     string   `json:"updated_at"`
}

type Account struct {
	ID              int64  `json:"id"`
	Alias           string `json:"alias"`
	SourceID        string `json:"source_id"`
	CredentialID    *int64 `json:"credential_id,omitempty"`
	CredentialAlias string `json:"credential_alias,omitempty"`
	ProxyMode       string `json:"proxy_mode"`
	ProxyID         *int64 `json:"proxy_id,omitempty"`
	ProxyAlias      string `json:"proxy_alias,omitempty"`
	Enabled         bool   `json:"enabled"`
	Priority        int    `json:"priority"`
	State           string `json:"state"`
	LastError       string `json:"last_error,omitempty"`
	LastUsedAt      string `json:"last_used_at,omitempty"`
	LastSuccessAt   string `json:"last_success_at,omitempty"`
	UpdatedAt       string `json:"updated_at"`
}
