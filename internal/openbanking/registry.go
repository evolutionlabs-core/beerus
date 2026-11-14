package openbanking

import "time"

type RegistryClient struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	LegalName    string    `json:"legal_name"`
	RegistryID   string    `json:"registry_id"`
	CAC          string    `json:"cac"`
	Role         string    `json:"role"`
	CertCN       string    `json:"cert_cn"`
	JWK          JWK       `json:"jwk"`
	RedirectURIs []string  `json:"redirect_uris"`
	RegisteredAt time.Time `json:"registered_at"`
}
