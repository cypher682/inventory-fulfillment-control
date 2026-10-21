package inventory

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
)

type tenantContextKey struct{}

func tenantFromContext(ctx context.Context) (string, bool) {
	tenant, ok := ctx.Value(tenantContextKey{}).(string)
	return tenant, ok && tenant != ""
}

func (s *Server) authenticate(r *http.Request) (string, int) {
	provided := strings.TrimSpace(r.Header.Get("X-API-Key"))
	if provided == "" {
		authorization := strings.TrimSpace(r.Header.Get("Authorization"))
		if len(authorization) > 7 && strings.EqualFold(authorization[:7], "bearer ") {
			provided = strings.TrimSpace(authorization[7:])
		}
	}
	if provided == "" {
		return "", http.StatusUnauthorized
	}

	var matched string
	for tenant, key := range s.apiKeys {
		if subtle.ConstantTimeCompare([]byte(provided), []byte(key)) == 1 {
			matched = tenant
		}
	}
	if matched == "" {
		return "", http.StatusUnauthorized
	}

	if headerTenant := strings.TrimSpace(r.Header.Get("X-Tenant-ID")); headerTenant != "" && headerTenant != matched {
		return "", http.StatusForbidden
	}
	return matched, 0
}
