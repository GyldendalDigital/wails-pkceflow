package wailspkceflow

import (
	"testing"
	"time"

	pkceflow "github.com/GyldendalDigital/go-pkceflow"
)

func TestNewClaimsDTO(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	c := pkceflow.Claims{
		Subject:           "sub-123",
		Name:              "Ada Lovelace",
		GivenName:         "Ada",
		FamilyName:        "Lovelace",
		PreferredUsername: "ada",
		Email:             "ada@example.com",
		EmailVerified:     true,
		Issuer:            "https://idp.example.com",
		Audience:          []string{"my-app"},
		ExpiresAt:         now,
		IssuedAt:          now,
		AuthTime:          now,
		Raw:               map[string]any{"groups": []any{"admins"}},
	}

	dto := newClaimsDTO(&c)

	if dto.Subject != "sub-123" || dto.Email != "ada@example.com" || !dto.EmailVerified {
		t.Errorf("scalar fields not copied: %+v", dto)
	}
	if len(dto.Audience) != 1 || dto.Audience[0] != "my-app" {
		t.Errorf("Audience = %v", dto.Audience)
	}
	if !dto.ExpiresAt.Equal(now) {
		t.Errorf("ExpiresAt = %v, want %v", dto.ExpiresAt, now)
	}
	if dto.Raw["groups"] == nil {
		t.Error("Raw not carried through")
	}
}
