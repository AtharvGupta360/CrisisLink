package dispatch

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/AtharvGupta360/CrisisLink/internal/incident"
	"github.com/AtharvGupta360/CrisisLink/internal/outbox"
	"github.com/AtharvGupta360/CrisisLink/internal/unit"
)

// ErrNoReassignCandidate means nothing suitable was free to take over.
var ErrNoReassignCandidate = errors.New("no candidate available for reassignment")

// Reassign atomically ENDS one dispatch and BEGINS another. It backs both
// reassignment flows, which differ only in what changes:
//
//	REROUTE   same incident, different unit  — the assigned unit failed us
//	PREEMPT   same unit, different incident  — a more urgent incident took it
//
// Everything happens in ONE transaction, because every intermediate state is
// invalid: a cancelled dispatch whose replacement failed leaves an incident with no
// help, and a freed unit that was never re-reserved can be grabbed by someone else
// in the gap.
//
// # LOCK ORDERING — the new hazard in this phase
//
// This is the first operation that locks TWO unit rows. If transaction A locks unit
// X then waits for Y while B locks Y then waits for X, neither can proceed — a
// classic deadlock, which Postgres resolves by killing one of them.
//
// The fix is discipline, not cleverness: every transaction needing both locks takes
// them in the SAME order. Sorting by id gives a total order that every caller
// computes identically without coordinating, so a wait cycle cannot form. Locking
// "old then new" would NOT work — that is precisely the swap case that deadlocks.
func (r *DispatchRepository) Reassign(ctx context.Context, oldDispatchID, newIncidentID, newUnitID, eventType string) (*Dispatch, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// 1. Lock the dispatch being ended and re-check it under the lock: two operators
	//    could reroute the same dispatch at once, and only one may win.
	var old Dispatch
	if err = scanDispatch(tx.QueryRow(ctx,
		`SELECT `+dispatchColumns+` FROM dispatches WHERE id = $1::uuid FOR UPDATE`, oldDispatchID,
	), &old); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrDispatchNotFound
		}
		return nil, err
	}
	if !IsActiveDispatch(old.Status) {
		return nil, ErrIllegalDispatchTransition // already completed or cancelled
	}

	sameUnit := old.UnitID == newUnitID

	// 2. Lock the unit row(s) in a deterministic order (see the note above).
	if sameUnit {
		if _, err = r.units.LockForReserveTx(ctx, tx, old.UnitID); err != nil {
			return nil, err
		}
	} else {
		first, second := old.UnitID, newUnitID
		if first > second {
			first, second = second, first
		}
		if _, err = r.units.LockForReserveTx(ctx, tx, first); err != nil {
			return nil, err
		}
		if _, err = r.units.LockForReserveTx(ctx, tx, second); err != nil {
			return nil, err
		}

		// Re-check the incoming unit while holding its lock. It was chosen by a
		// scoring pass that ran OUTSIDE this transaction, so it may have been taken
		// since: the decision is made optimistically, the commitment verified
		// pessimistically.
		newStatus, serr := r.units.LockForReserveTx(ctx, tx, newUnitID)
		if serr != nil {
			return nil, serr
		}
		if newStatus != unit.UnitAvailable {
			return nil, ErrUnitNotAvailable
		}
	}

	// 3. The incoming incident must still be worth dispatching to.
	incStatus, err := r.incidents.StatusTx(ctx, tx, newIncidentID)
	if err != nil {
		return nil, err
	}
	if incStatus == incident.StatusResolved || incStatus == incident.StatusCancelled {
		return nil, ErrIncidentNotDispatchable
	}

	// 4. End the old dispatch.
	if _, err = tx.Exec(ctx,
		`UPDATE dispatches SET status = 'cancelled', updated_at = now() WHERE id = $1::uuid`,
		oldDispatchID,
	); err != nil {
		return nil, err
	}

	// 5. Move the unit. When the unit is unchanged (preemption) it simply STAYS
	//    reserved — freeing and re-reserving would open a window in which another
	//    transaction could take it from us.
	if !sameUnit {
		if err = r.units.SetStatusTx(ctx, tx, old.UnitID, unit.UnitAvailable); err != nil {
			return nil, err
		}
		if err = r.units.MarkReservedTx(ctx, tx, newUnitID); err != nil {
			return nil, err
		}
	}

	// 6. Create the replacement. The partial unique index (one active dispatch per
	//    unit) is the structural backstop: a 23505 means someone raced past the lock
	//    logic, and is treated exactly like "unit not available".
	var nd Dispatch
	if err = tx.QueryRow(ctx,
		`INSERT INTO dispatches (incident_id, unit_id)
		 VALUES ($1::uuid, $2::uuid)
		 RETURNING id::text, incident_id::text, unit_id::text, status, created_at, updated_at`,
		newIncidentID, newUnitID,
	).Scan(&nd.ID, &nd.IncidentID, &nd.UnitID, &nd.Status, &nd.CreatedAt, &nd.UpdatedAt); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrUnitNotAvailable
		}
		return nil, err
	}

	if err = r.incidents.MarkDispatchedTx(ctx, tx, newIncidentID); err != nil {
		return nil, err
	}

	// 7. If the OLD incident just lost its last active dispatch, send it back to
	//    'verified' so it resurfaces as needing help. Without this, a preempted
	//    incident would sit in 'dispatched' with nobody actually coming.
	if old.IncidentID != newIncidentID {
		var stillActive int
		if err = tx.QueryRow(ctx,
			`SELECT count(*) FROM dispatches
			  WHERE incident_id = $1::uuid AND status IN ('reserved','en_route','on_scene')`,
			old.IncidentID,
		).Scan(&stillActive); err != nil {
			return nil, err
		}
		if stillActive == 0 {
			if err = r.incidents.RevertToVerifiedTx(ctx, tx, old.IncidentID); err != nil {
				return nil, err
			}
		}
	}

	// 8. Emit the event in the same transaction. The payload names BOTH sides so a
	//    consumer can reconstruct what moved without querying back.
	if err = r.events.WriteTx(ctx, tx, outbox.AggregateDispatch, nd.ID, eventType, map[string]any{
		"dispatchId":         nd.ID,
		"incidentId":         nd.IncidentID,
		"unitId":             nd.UnitID,
		"replacedDispatchId": old.ID,
		"previousIncidentId": old.IncidentID,
		"previousUnitId":     old.UnitID,
	}); err != nil {
		return nil, err
	}

	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &nd, nil
}

// Preemptable is an active dispatch whose unit could be taken for a more urgent
// incident.
type Preemptable struct {
	DispatchID     string  `json:"dispatchId"`
	UnitID         string  `json:"unitId"`
	IncidentID     string  `json:"incidentId"`
	Severity       string  `json:"severity"`
	CallSign       string  `json:"callSign"`
	DistanceMeters float64 `json:"distanceMeters"`
}

// The severity CASE below is written inline rather than stored as a numeric column
// because the severity vocabulary belongs to the incident domain; this is a
// query-time projection of it, not a second source of truth to keep in sync.
//
// FindPreemptable returns active dispatches whose incident is STRICTLY less severe
// than the given severity, nearest to (lat,lng) first — the units that may be taken
// for a more urgent call.
//
// The ranking and comparison happen IN the query because they drive its ORDER BY
// and LIMIT. Fetching every active dispatch to rank them in Go would not scale, and
// would widen the window in which the data changes underneath the decision.
func (r *DispatchRepository) FindPreemptable(ctx context.Context, severity string, lat, lng float64, limit int) ([]Preemptable, error) {
	const q = `
		SELECT d.id::text, d.unit_id::text, d.incident_id::text, i.severity, u.call_sign,
		       ST_Distance(u.location::geography, ST_SetSRID(ST_MakePoint($2, $3), 4326)::geography) AS distance_m
		FROM dispatches d
		JOIN incidents i ON i.id = d.incident_id
		JOIN units u     ON u.id = d.unit_id
		WHERE d.status IN ('reserved','en_route','on_scene')
		  AND (CASE i.severity WHEN 'critical' THEN 4 WHEN 'high' THEN 3 WHEN 'medium' THEN 2 ELSE 1 END)
		    < (CASE $1::text  WHEN 'critical' THEN 4 WHEN 'high' THEN 3 WHEN 'medium' THEN 2 ELSE 1 END)
		ORDER BY distance_m
		LIMIT $4`
	rows, err := r.pool.Query(ctx, q, severity, lng, lat, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Preemptable, 0, limit)
	for rows.Next() {
		var p Preemptable
		if err := rows.Scan(&p.DispatchID, &p.UnitID, &p.IncidentID, &p.Severity, &p.CallSign, &p.DistanceMeters); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
