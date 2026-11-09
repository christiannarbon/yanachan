// Package access decides which GitHub users may use a server-mode deployment.
package access

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/christiannarbon/yanachan-4k/backend/internal/config"
)

// ReverifyAfter is how long a successful check is trusted before an existing
// session is checked again.
const ReverifyAfter = 12 * time.Hour

var ErrNotAllowed = errors.New("not allowed to use this deployment")

// Policy admits a user who is in Users (by login), in UserIDs (by GitHub's
// numeric ID, which unlike a login can never be renamed and re-registered by
// someone else), a member of one of Orgs, or anyone when AllowAny is set.
type Policy struct {
	Orgs     []string
	Users    []string
	UserIDs  []int64
	AllowAny bool
}

// IDPrefix marks an allowlist entry as a numeric user ID: "id:583231".
const IDPrefix = "id:"

// ParseUserID reads an "id:<n>" entry. ok is false for a login.
func ParseUserID(entry string) (id int64, ok bool, err error) {
	rest, found := strings.CutPrefix(entry, IDPrefix)
	if !found {
		return 0, false, nil
	}
	id, err = strconv.ParseInt(rest, 10, 64)
	if err != nil || id <= 0 {
		return 0, true, fmt.Errorf("%q is not id:<positive number>", entry)
	}
	return id, true, nil
}

// PolicyFromConfig assumes cfg passed Validate, which rejects malformed
// id: entries.
func PolicyFromConfig(cfg config.Config) Policy {
	p := Policy{
		Orgs:     append([]string(nil), cfg.AllowedOrgs...),
		AllowAny: cfg.AllowAnyUser,
	}
	for _, u := range cfg.AllowedUsers {
		if id, isID, err := ParseUserID(u); isID {
			if err == nil {
				p.UserIDs = append(p.UserIDs, id)
			}
			continue
		}
		p.Users = append(p.Users, u)
	}
	return p
}

// Member is the slice of github.Client that Check needs; tests may fake it.
type Member interface {
	OrgMembership(ctx context.Context, orgs []string) (map[string]bool, error)
}

// Check returns nil, ErrNotAllowed, or the error that stopped GitHub from
// answering. Callers must not treat the last as a refusal.
func (p Policy) Check(ctx context.Context, cl Member, login string, id int64) error {
	if p.AllowAny {
		return nil
	}
	for _, u := range p.UserIDs {
		if u == id {
			return nil
		}
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
