// Package access decides which GitHub users may use a server-mode deployment.
package access

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/christiannarbon/yanachan-4k/backend/internal/config"
)

// ReverifyAfter is how long a successful check is trusted before an existing
// session is checked again.
const ReverifyAfter = 12 * time.Hour

var ErrNotAllowed = errors.New("not allowed to use this deployment")

type Policy struct {
	Orgs     []string
	Users    []string
	AllowAny bool
}

func PolicyFromConfig(cfg config.Config) Policy {
	return Policy{
		Orgs:     append([]string(nil), cfg.AllowedOrgs...),
		Users:    append([]string(nil), cfg.AllowedUsers...),
		AllowAny: cfg.AllowAnyUser,
	}
}

// Member is the slice of github.Client that Check needs; tests may fake it.
type Member interface {
	OrgMembership(ctx context.Context, orgs []string) (map[string]bool, error)
}

// Check returns nil, ErrNotAllowed, or the error that stopped GitHub from
// answering. Callers must not treat the last as a refusal.
func (p Policy) Check(ctx context.Context, cl Member, login string) error {
	if p.AllowAny {
		return nil
	}
	for _, u := range p.Users {
		if strings.EqualFold(u, login) {
			return nil
		}
	}
	if len(p.Orgs) == 0 {
		return ErrNotAllowed
	}
	member, err := cl.OrgMembership(ctx, p.Orgs)
	if err != nil {
		return fmt.Errorf("check org membership: %w", err)
	}
	for _, org := range p.Orgs {
		if member[org] {
			return nil
		}
	}
	return ErrNotAllowed
}
