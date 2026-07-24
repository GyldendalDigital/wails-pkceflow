package wailspkceflow

import (
	"time"

	pkceflow "github.com/GyldendalDigital/go-pkceflow"
)

// ClaimsDTO is a frontend-facing view of the ID token claims. It carries stable
// JSON field names so the Wails binding generator produces consistent
// TypeScript types. It never includes any token.
type ClaimsDTO struct {
	Subject           string    `json:"subject"`
	Name              string    `json:"name"`
	GivenName         string    `json:"givenName"`
	FamilyName        string    `json:"familyName"`
	PreferredUsername string    `json:"preferredUsername"`
	Email             string    `json:"email"`
	EmailVerified     bool      `json:"emailVerified"`
	Issuer            string    `json:"issuer"`
	Audience          []string  `json:"audience"`
	ExpiresAt         time.Time `json:"expiresAt"`
	IssuedAt          time.Time `json:"issuedAt"`
	AuthTime          time.Time `json:"authTime"`

	// Raw holds every claim in the ID token payload, including provider-specific
	// claims not mapped to a typed field above (for example groups or roles).
	Raw map[string]any `json:"raw,omitempty"`
}

// newClaimsDTO converts core claims to the frontend DTO.
func newClaimsDTO(c *pkceflow.Claims) ClaimsDTO {
	return ClaimsDTO{
		Subject:           c.Subject,
		Name:              c.Name,
		GivenName:         c.GivenName,
		FamilyName:        c.FamilyName,
		PreferredUsername: c.PreferredUsername,
		Email:             c.Email,
		EmailVerified:     c.EmailVerified,
		Issuer:            c.Issuer,
		Audience:          c.Audience,
		ExpiresAt:         c.ExpiresAt,
		IssuedAt:          c.IssuedAt,
		AuthTime:          c.AuthTime,
		Raw:               c.Raw,
	}
}
