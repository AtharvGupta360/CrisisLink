// Package authz holds the authorization vocabulary: who can exist, and how to
// describe the caller making a request.
//
// It lives in platform rather than in the auth module because BOTH auth (which
// issues roles) and middleware (which enforces them) need it, and platform is
// allowed to be imported by everything. Putting it in auth would force the
// middleware to import a domain module, inverting the layering.
package authz

// The role vocabulary. Five roles, four of them operational plus a system role.
// They are mirrored by a CHECK constraint on users.role, so an unknown role cannot
// be persisted even if application code is wrong.
const (
	// RoleCitizen is the public: report incidents, track your own, find shelters.
	RoleCitizen = "citizen"

	// RoleResponder is a rescue team member in the field. Bound to ONE unit
	// (users.unit_id) and may only act for that unit.
	RoleResponder = "responder"

	// RoleShelterManager runs one shelter. Bound to it via users.shelter_id.
	RoleShelterManager = "shelter_manager"

	// RoleOperator is the control room: verify incidents, dispatch units, book
	// transport. The coordination role, not bound to any single resource.
	RoleOperator = "operator"

	// RoleAdmin administers the system: fleet/shelter/transport registries, the
	// outbox and dead-letter queue, and user role assignment.
	RoleAdmin = "admin"
)

// All is the complete vocabulary, in ascending order of privilege.
var All = []string{RoleCitizen, RoleResponder, RoleShelterManager, RoleOperator, RoleAdmin}

// IsValid reports whether a role is one this system recognises. Used when an admin
// assigns a role, so a typo is rejected by the API before the CHECK constraint has
// to catch it.
func IsValid(role string) bool {
	for _, r := range All {
		if r == role {
			return true
		}
	}
	return false
}

// Actor is the caller's identity as far as authorization is concerned. It is built
// from the verified JWT claims, never from request input.
//
// UnitID/ShelterID are the OWNERSHIP bindings. They are empty for roles that are
// not bound to a resource, and they are what turns "is this user a responder?"
// into "is this user the responder for THIS unit?".
type Actor struct {
	UserID    string
	Username  string
	Role      string
	UnitID    string
	ShelterID string
}

// Is reports whether the actor holds any of the given roles.
func (a Actor) Is(roles ...string) bool {
	for _, r := range roles {
		if a.Role == r {
			return true
		}
	}
	return false
}

// OwnsUnit reports whether this actor may act FOR a specific unit.
//
// Operators and admins are not bound to a unit and may act for any of them — that
// is the point of a control room. A responder may act only for the unit they are
// assigned to. Anyone else may not act for a unit at all.
func (a Actor) OwnsUnit(unitID string) bool {
	if a.Is(RoleOperator, RoleAdmin) {
		return true
	}
	// An unbound responder owns nothing: an empty UnitID must never match an empty
	// resource id and accidentally grant access.
	return a.Is(RoleResponder) && a.UnitID != "" && a.UnitID == unitID
}

// OwnsShelter reports whether this actor may act FOR a specific shelter, with the
// same rules as OwnsUnit.
func (a Actor) OwnsShelter(shelterID string) bool {
	if a.Is(RoleOperator, RoleAdmin) {
		return true
	}
	return a.Is(RoleShelterManager) && a.ShelterID != "" && a.ShelterID == shelterID
}
