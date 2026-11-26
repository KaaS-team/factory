//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package admin

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/KaaS-team/factory/ee/authz"
	"github.com/KaaS-team/factory/ee/contract"
)

// LocalUser is a principal managed in the console, such as a CI service account. SCIM users are
// not here: their roles come from IdP groups and the console shows them read-only.
type LocalUser struct {
	ID       string                 `json:"id"`
	Name     string                 `json:"name"`
	Kind     contract.PrincipalKind `json:"kind"`
	Role     contract.Role          `json:"role"`
	Disabled bool                   `json:"disabled,omitempty"`
}

// LocalUsers is the console's own users, in a JSON file, or in memory when Path is empty.
type LocalUsers struct {
	Path string

	mu    sync.Mutex
	users []LocalUser
	read  bool
}

// errUnknownUser is a role or status change for an id the console does not manage.
var errUnknownUser = errors.New("admin: no console-managed user has that id")

func (l *LocalUsers) load() error {
	if l.read || l.Path == "" {
		l.read = true
		return nil
	}
	data, err := os.ReadFile(l.Path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(data, &l.users); err != nil {
			return fmt.Errorf("admin: %s: %w", l.Path, err)
		}
	}
	l.read = true
	return nil
}

func (l *LocalUsers) save() error {
	if l.Path == "" {
		return nil
	}
	data, err := json.MarshalIndent(l.users, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(l.Path, data)
}

// List returns the users in the order they were added.
func (l *LocalUsers) List() ([]LocalUser, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.load(); err != nil {
		return nil, err
	}
	return slices.Clone(l.users), nil
}

// Principal is the contract form of a console user: its role bound at org scope.
func (u LocalUser) Principal(org string) contract.Principal {
	return contract.Principal{ID: u.ID, Kind: u.Kind, Org: org, Disabled: u.Disabled,
		Roles: []contract.RoleBinding{{Role: u.Role, Scope: contract.Scope{Kind: contract.ScopeOrg, ID: org}}}}
}

// Add stores a new user and returns it with its id.
func (l *LocalUsers) Add(u LocalUser) (LocalUser, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.load(); err != nil {
		return u, err
	}
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return u, err
	}
	u.ID = string(u.Kind) + "-" + hex.EncodeToString(b)
	l.users = append(l.users, u)
	return u, l.save()
}

// update applies fn to the user with id and saves.
func (l *LocalUsers) update(id string, fn func(*LocalUser)) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.load(); err != nil {
		return err
	}
	i := slices.IndexFunc(l.users, func(u LocalUser) bool { return u.ID == id })
	if i < 0 {
		return errUnknownUser
	}
	fn(&l.users[i])
	return l.save()
}

// userRow is one line of the users table.
type userRow struct {
	ID, Name, Kind, Source, Roles, Via string
	Disabled                           bool
	Editable                           bool
	Role                               string
}

// assignable is the roles the console may give. Bot is minted per run, never assigned by hand.
var assignable = []string{string(contract.RoleOwner), string(contract.RoleAdmin), string(contract.RoleMember), string(contract.RoleViewer)}

type usersData struct {
	SCIM, Local []userRow
	SCIMError   string
	LocalOff    bool
	CanEdit     bool
	Roles       []string
	Kinds       []string
}

func (c *Console) users(w http.ResponseWriter, r *http.Request, p contract.Principal) {
	d := usersData{Roles: assignable, Kinds: []string{string(contract.KindService), string(contract.KindUser)}}
	d.CanEdit = c.cfg.Authorizer.Can(r.Context(), p, contract.ActionAdminUsers, contract.Resource{Org: c.cfg.Org}).Allow
	if c.cfg.SCIM != nil {
		us, err := c.cfg.SCIM.ListUsers(c.cfg.Org)
		if err != nil {
			d.SCIMError = "The SCIM directory could not be read."
		}
		for _, u := range us {
			pr, err := c.cfg.Identity.LookupBySCIM(r.Context(), u.ID)
			if err != nil {
				pr = contract.Principal{ID: u.ID, Disabled: !u.Active}
			}
			d.SCIM = append(d.SCIM, userRow{ID: u.ID, Name: u.UserName, Kind: string(contract.KindUser), Source: "SCIM",
				Roles: roles(pr.Roles), Via: c.groupsOf(u.ID), Disabled: pr.Disabled || !u.Active})
		}
	}
	if c.cfg.Local == nil {
		d.LocalOff = true
	} else {
		ls, err := c.cfg.Local.List()
		if err != nil {
			c.page.error(w, http.StatusInternalServerError, "Users could not be read", "The console user file could not be read.")
			return
		}
		for _, u := range ls {
			d.Local = append(d.Local, userRow{ID: u.ID, Name: u.Name, Kind: string(u.Kind), Source: "Console",
				Roles: roles(u.Principal(c.cfg.Org).Roles), Role: string(u.Role), Disabled: u.Disabled, Editable: d.CanEdit})
		}
	}
	c.page.render(w, http.StatusOK, "users", "Users and roles", notice(r), d)
}

// roles prints bindings as "admin (org)", one per binding.
func roles(bs []contract.RoleBinding) string {
	if len(bs) == 0 {
		return "none"
	}
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = fmt.Sprintf("%s (%s %s)", b.Role, b.Scope.Kind, b.Scope.ID)
	}
	return strings.Join(out, ", ")
}

// groupsOf names the SCIM groups of a user that the group map turns into roles.
func (c *Console) groupsOf(id string) string {
	gs, err := c.cfg.SCIM.ListGroups(c.cfg.Org)
	if err != nil {
		return ""
	}
	var names []string
	for _, g := range gs {
		if slices.Contains(g.Members, id) && slices.ContainsFunc(c.cfg.Groups, func(m authz.GroupRole) bool { return m.Group == g.DisplayName }) {
			names = append(names, g.DisplayName)
		}
	}
	return strings.Join(names, ", ")
}

func (c *Console) addUser(w http.ResponseWriter, r *http.Request, p contract.Principal) {
	if c.cfg.Local == nil {
		c.page.error(w, http.StatusNotFound, "Console users are off", "Set a console user file to manage users here.")
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	kind := contract.PrincipalKind(r.PostFormValue("kind"))
	role := contract.Role(r.PostFormValue("role"))
	if name == "" || !slices.Contains(assignable, string(role)) || (kind != contract.KindService && kind != contract.KindUser) {
		c.page.error(w, http.StatusUnprocessableEntity, "The user was not added", "Give a name, a kind (service or user) and one of the roles owner, admin, member or viewer.")
		return
	}
	data := map[string]string{"user.name": name, "user.kind": string(kind), "role": string(role)}
	if err := c.change(r.Context(), p, contract.ActionAdminUsers, data, func() error {
		_, err := c.cfg.Local.Add(LocalUser{Name: name, Kind: kind, Role: role})
		return err
	}); err != nil {
		c.fail(w, "The user was not added")
		return
	}
	done(w, r, "/admin/users", "user-added")
}

func (c *Console) setRole(w http.ResponseWriter, r *http.Request, p contract.Principal) {
	id, role := r.PostFormValue("id"), contract.Role(r.PostFormValue("role"))
	if !slices.Contains(assignable, string(role)) {
		c.page.error(w, http.StatusUnprocessableEntity, "The role was not changed", "Pick one of the roles owner, admin, member or viewer.")
		return
	}
	c.localChange(w, r, p, id, "role-changed", map[string]string{"user": id, "role": string(role)}, func(u *LocalUser) { u.Role = role })
}

func (c *Console) setStatus(w http.ResponseWriter, r *http.Request, p contract.Principal) {
	id, off := r.PostFormValue("id"), r.PostFormValue("disabled") == "true"
	if id == p.ID && off {
		c.page.error(w, http.StatusUnprocessableEntity, "You cannot disable yourself", "Ask another owner to do it, so the org keeps one.")
		return
	}
	c.localChange(w, r, p, id, "status-change", map[string]string{"user": id, "disabled": fmt.Sprint(off)}, func(u *LocalUser) { u.Disabled = off })
}

// localChange checks that id is a console user (SCIM users are read-only), then records and applies fn.
func (c *Console) localChange(w http.ResponseWriter, r *http.Request, p contract.Principal, id, key string, data map[string]string, fn func(*LocalUser)) {
	if c.cfg.Local == nil {
		c.page.error(w, http.StatusNotFound, "Console users are off", "Set a console user file to manage users here.")
		return
	}
	ls, err := c.cfg.Local.List()
	if err != nil || !slices.ContainsFunc(ls, func(u LocalUser) bool { return u.ID == id }) {
		c.page.error(w, http.StatusNotFound, "No such console user", "SCIM users are managed in your identity provider. Change their groups there.")
		return
	}
	if err := c.change(r.Context(), p, contract.ActionAdminUsers, data, func() error { return c.cfg.Local.update(id, fn) }); err != nil {
		c.fail(w, "The user was not changed")
		return
	}
	done(w, r, "/admin/users", key)
}
