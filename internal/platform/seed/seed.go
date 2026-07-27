// Package seed populates a fresh database with the demo accounts and fixtures the
// public console needs to be explorable.
//
// WHY THIS EXISTS AS CODE rather than the shell script in scripts/: that script
// promotes roles with `docker exec … psql`, which is impossible against a managed
// database with no shell access. On a free host the only way in is the app itself,
// so seeding has to live here.
//
// It is GATED on DEMO_SEED=true and is idempotent (every insert is ON CONFLICT DO
// NOTHING), so it can run on every boot of an ephemeral container without
// duplicating anything — and it does nothing at all unless explicitly asked.
//
// The accounts it creates are PUBLIC and share a well-known password. That is
// acceptable only because this is a demo with disposable data; the flag must never
// be set on anything holding real information.
package seed

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AtharvGupta360/CrisisLink/internal/auth"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/authz"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/common"
)

// DemoPassword is shared by every seeded account and is intentionally public —
// the login screen offers these accounts as one-click buttons.
const DemoPassword = "password123"

type demoUser struct {
	username, email, role string
}

var demoUsers = []demoUser{
	{"demo_citizen", "citizen@crisislink.dev", authz.RoleCitizen},
	{"demo_responder", "responder@crisislink.dev", authz.RoleResponder},
	{"demo_shelter", "shelter@crisislink.dev", authz.RoleShelterManager},
	{"demo_operator", "operator@crisislink.dev", authz.RoleOperator},
	{"demo_admin", "admin@crisislink.dev", authz.RoleAdmin},
}

type demoUnit struct {
	callSign, kind string
	lat, lng       float64
}

var demoUnits = []demoUnit{
	{"DEMO-AMB-1", "ambulance", 28.6200, 77.2100},
	{"DEMO-AMB-2", "ambulance", 28.6050, 77.2200},
	{"DEMO-FIRE-1", "fire", 28.6300, 77.1950},
	{"DEMO-RESC-1", "rescue", 28.6100, 77.2300},
}

type demoIncident struct {
	title, severity string
	lat, lng        float64
}

// Spread apart deliberately: reports within ~200 m of each other inside the dedupe
// window would be MERGED into one incident, which is correct behaviour but would
// leave the demo with a single row.
var demoIncidents = []demoIncident{
	{"Building collapse", "critical", 28.6190, 77.2110},
	{"Gas leak", "high", 28.6060, 77.2210},
	{"Road flooding", "medium", 28.6310, 77.1960},
	{"Fallen tree", "low", 28.6110, 77.2310},
}

// Run seeds demo data. Safe to call on every boot: it is idempotent and returns
// nil when there is nothing to do.
func Run(ctx context.Context, pool *pgxpool.Pool) error {
	hash, err := auth.HashPassword(DemoPassword)
	if err != nil {
		return fmt.Errorf("seed: hash password: %w", err)
	}

	// --- users ---------------------------------------------------------------
	for _, u := range demoUsers {
		if _, err := pool.Exec(ctx,
			`INSERT INTO users (username, email, password, role)
			 VALUES ($1, $2, $3, $4)
			 ON CONFLICT (email) DO NOTHING`,
			u.username, u.email, hash, u.role,
		); err != nil {
			return fmt.Errorf("seed user %s: %w", u.email, err)
		}
	}

	// --- fleet ---------------------------------------------------------------
	for _, u := range demoUnits {
		if _, err := pool.Exec(ctx,
			`INSERT INTO units (call_sign, type, location)
			 VALUES ($1, $2, ST_SetSRID(ST_MakePoint($3, $4), 4326))
			 ON CONFLICT (call_sign) DO NOTHING`,
			u.callSign, u.kind, u.lng, u.lat, // ST_MakePoint is (x=lng, y=lat)
		); err != nil {
			return fmt.Errorf("seed unit %s: %w", u.callSign, err)
		}
	}

	// --- shelters ------------------------------------------------------------
	shelters := []struct {
		name     string
		capacity int
		lat, lng float64
	}{
		{"Demo Community Hall", 20, 28.6180, 77.2150},
		{"Demo School Gym", 40, 28.6000, 77.2250},
	}
	for _, s := range shelters {
		if _, err := pool.Exec(ctx,
			`INSERT INTO shelters (name, capacity, location)
			 SELECT $1, $2, ST_SetSRID(ST_MakePoint($3, $4), 4326)
			 WHERE NOT EXISTS (SELECT 1 FROM shelters WHERE name = $1)`,
			s.name, s.capacity, s.lng, s.lat,
		); err != nil {
			return fmt.Errorf("seed shelter %s: %w", s.name, err)
		}
	}

	// --- ownership bindings --------------------------------------------------
	// These are the claims that make object-level authorization demonstrable: the
	// responder may act for exactly one unit, the manager for exactly one shelter.
	if _, err := pool.Exec(ctx,
		`UPDATE users SET unit_id = (SELECT id FROM units WHERE call_sign = 'DEMO-AMB-1')
		  WHERE email = 'responder@crisislink.dev' AND unit_id IS NULL`,
	); err != nil {
		return fmt.Errorf("seed responder binding: %w", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE users SET shelter_id = (SELECT id FROM shelters WHERE name = 'Demo Community Hall')
		  WHERE email = 'shelter@crisislink.dev' AND shelter_id IS NULL`,
	); err != nil {
		return fmt.Errorf("seed shelter binding: %w", err)
	}

	// --- people awaiting placement -------------------------------------------
	for _, name := range []string{"Asha Kumari", "Ravi Sharma", "Priya Nair", "Dev Menon"} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO victims (name, location)
			 SELECT $1, ST_SetSRID(ST_MakePoint($2, $3), 4326)
			 WHERE NOT EXISTS (SELECT 1 FROM victims WHERE name = $1)`,
			name, 77.209, 28.614,
		); err != nil {
			return fmt.Errorf("seed victim %s: %w", name, err)
		}
	}

	// --- incidents -----------------------------------------------------------
	for _, i := range demoIncidents {
		if _, err := pool.Exec(ctx,
			`INSERT INTO incidents (reporter_id, title, description, severity, location)
			 SELECT (SELECT id FROM users WHERE email = 'citizen@crisislink.dev'),
			        $1, 'Seeded demo incident.', $2,
			        ST_SetSRID(ST_MakePoint($3, $4), 4326)
			 WHERE NOT EXISTS (SELECT 1 FROM incidents WHERE title = $1)`,
			i.title, i.severity, i.lng, i.lat,
		); err != nil {
			return fmt.Errorf("seed incident %s: %w", i.title, err)
		}
	}

	common.Logger.Infow("demo data seeded",
		"users", len(demoUsers), "units", len(demoUnits), "incidents", len(demoIncidents))
	return nil
}
