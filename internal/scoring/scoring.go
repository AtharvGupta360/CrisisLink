// Package scoring holds the dispatch scoring function. It is deliberately PURE:
// every function here is a plain input -> output mapping with no DB, clock,
// randomness, or I/O. That is what makes the ranking (a) trivially unit-testable
// with no mocks and (b) explainable — the final score is a weighted sum of named
// components, each of which we surface in the Breakdown.
package scoring

import "github.com/AtharvGupta360/CrisisLink/internal/models"

// Component weights. They sum to 1.0, so a Score is always in [0, 1] and the
// weights read as "proximity is 70% of the decision, type match 30%."
const (
	weightProximity = 0.70
	weightTypeMatch = 0.30
)

// proximityFalloffMeters is the distance at which the proximity component decays
// to 0. A unit farther than this contributes no proximity, but can still win on
// type match — it just needs to be the right kind of help.
const proximityFalloffMeters = 10000.0 // 10 km

// partialTypeMatch is the credit a non-preferred unit receives. It is > 0 because
// a wrong-type unit can still respond as a fallback; it is < 1 so the preferred
// type is always favored, all else equal.
const partialTypeMatch = 0.30

// Breakdown is the explainable trace of a single Score. Each field is the value
// of a component BEFORE weighting (except DistanceMeters, the raw input), so a
// reviewer can reconstruct exactly why one unit outranked another.
type Breakdown struct {
	Proximity      float64 `json:"proximity"`               // 0..1, closer = higher
	TypeMatch      float64 `json:"typeMatch"`               // 0..1, preferred type = 1.0
	DistanceMeters float64 `json:"distanceMeters"`          // raw KNN distance input
	PreferredType  string  `json:"preferredType,omitempty"` // "" = no preference
}

// ScoredUnit pairs a unit with its dispatch score and the breakdown that produced
// it. This is what the API returns instead of a bare unit list.
type ScoredUnit struct {
	Unit      models.Unit `json:"unit"`
	Score     float64     `json:"score"`
	Breakdown Breakdown   `json:"breakdown"`
}

// Score computes a unit's dispatch score for an incident, given the dispatcher's
// preferred unit type ("" = no preference). PURE: same inputs -> same output.
//
// The incident is represented through unit.DistanceMeters, which the KNN search
// computed relative to that incident — so proximity already answers "how far is
// this unit from THIS event." Severity is constant across candidates and cannot
// change their relative order, so it is intentionally not a factor here.
func Score(unit *models.Unit, preferredType string) (float64, Breakdown) {
	// Proximity: linear decay from 1.0 (on top of the incident) to 0.0 at the
	// falloff. Clamp at 0 so far-away units don't go negative.
	prox := 1.0 - unit.DistanceMeters/proximityFalloffMeters
	if prox < 0 {
		prox = 0
	}

	// Type match: full credit for the preferred type, partial for anything else.
	// No preference => neutral 1.0, so Score degrades to pure proximity.
	typeMatch := 1.0
	if preferredType != "" && unit.Type != preferredType {
		typeMatch = partialTypeMatch
	}

	total := weightProximity*prox + weightTypeMatch*typeMatch

	return total, Breakdown{
		Proximity:      prox,
		TypeMatch:      typeMatch,
		DistanceMeters: unit.DistanceMeters,
		PreferredType:  preferredType,
	}
}
