package scoring

import (
	"math"
	"sort"
	"testing"

	"github.com/AtharvGupta360/CrisisLink/internal/incident"
	"github.com/AtharvGupta360/CrisisLink/internal/shelter"
	"github.com/AtharvGupta360/CrisisLink/internal/unit"
)

// eps is the float tolerance for comparisons — floats are never exactly equal.
const eps = 1e-9

func almostEqual(a, b float64) bool { return math.Abs(a-b) < eps }

// mkUnit is a tiny constructor so the tables stay readable. Named mkUnit, not
// unit, because `unit` is the package name after the modular split.
func mkUnit(typ string, dist float64) *unit.Unit {
	return &unit.Unit{Type: typ, Status: unit.UnitAvailable, DistanceMeters: dist}
}

func TestScoreUnit(t *testing.T) {
	tests := []struct {
		name      string
		unit      *unit.Unit
		need      Need
		wantSkill float64
		// wantScore is checked only where it is exactly derivable.
		checkScore bool
		wantScore  float64
	}{
		{
			name:      "on scene, right type, critical",
			unit:      mkUnit(unit.UnitTypeAmbulance, 0),
			need:      Need{Severity: incident.SeverityCritical, RequiredType: unit.UnitTypeAmbulance},
			wantSkill: 1.0,
			// distance 0 -> eta 0 -> timeScore 1; skill 1; any weights sum to 1
			checkScore: true,
			wantScore:  1.0,
		},
		{
			name:      "wrong type gets partial skill credit",
			unit:      mkUnit(unit.UnitTypeFire, 0),
			need:      Need{Severity: incident.SeverityCritical, RequiredType: unit.UnitTypeAmbulance},
			wantSkill: partialSkillMatch,
			// timeScore 1, skill 0.30, weights critical = .85/.15
			checkScore: true,
			wantScore:  0.85*1.0 + 0.15*partialSkillMatch,
		},
		{
			name:       "no requirement is neutral, score reduces to pure speed",
			unit:       mkUnit(unit.UnitTypeFire, 0),
			need:       Need{Severity: incident.SeverityLow},
			wantSkill:  1.0,
			checkScore: true,
			wantScore:  1.0,
		},
		{
			name:      "beyond the ETA falloff the time component clamps to zero",
			unit:      mkUnit(unit.UnitTypeAmbulance, 1_000_000), // 1000 km
			need:      Need{Severity: incident.SeverityHigh, RequiredType: unit.UnitTypeAmbulance},
			wantSkill: 1.0,
			// timeScore clamps to 0, so only the skill weight remains
			checkScore: true,
			wantScore:  0.25 * 1.0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, bd := ScoreUnit(tc.unit, tc.need)
			if !almostEqual(bd.SkillScore, tc.wantSkill) {
				t.Errorf("skill = %v, want %v", bd.SkillScore, tc.wantSkill)
			}
			if tc.checkScore && !almostEqual(got, tc.wantScore) {
				t.Errorf("score = %v, want %v", got, tc.wantScore)
			}
			if got < 0 || got > 1 {
				t.Errorf("score %v outside [0,1]", got)
			}
			if !almostEqual(bd.Weights.Time+bd.Weights.Skill, 1.0) {
				t.Errorf("weights must sum to 1, got %v", bd.Weights)
			}
		})
	}
}

// TestETAReflectsUnitSpeed is the reason ETA exists as a separate factor from raw
// distance: two units at the SAME distance must not be equally fast, because a
// heavy appliance is slower than an ambulance.
func TestETAReflectsUnitSpeed(t *testing.T) {
	const d = 5000.0
	need := Need{Severity: incident.SeverityHigh} // no type requirement: isolate speed

	ambScore, ambBD := ScoreUnit(mkUnit(unit.UnitTypeAmbulance, d), need)
	fireScore, fireBD := ScoreUnit(mkUnit(unit.UnitTypeFire, d), need)

	if fireBD.ETASeconds <= ambBD.ETASeconds {
		t.Fatalf("fire ETA (%v) should exceed ambulance ETA (%v) at equal distance",
			fireBD.ETASeconds, ambBD.ETASeconds)
	}
	if ambScore <= fireScore {
		t.Fatalf("faster unit should score higher at equal distance: amb=%v fire=%v", ambScore, fireScore)
	}
	// Both saw the same straight-line distance; only the travel model differs.
	if !almostEqual(ambBD.DistanceMeters, fireBD.DistanceMeters) {
		t.Fatal("distance input should be identical")
	}
}

// TestSeverityChangesTheTradeoff is the heart of the severity design. Severity is
// constant across candidates, so it cannot be an additive term — it re-weights.
// The SAME two candidates must rank differently under different severities:
// critical takes the closer wrong-type unit, low waits for the right specialist.
func TestSeverityChangesTheTradeoff(t *testing.T) {
	closeWrongType := mkUnit(unit.UnitTypeFire, 500)         // very close, wrong kind
	fartherRightType := mkUnit(unit.UnitTypeAmbulance, 6000) // farther, ideal kind

	critical := Need{Severity: incident.SeverityCritical, RequiredType: unit.UnitTypeAmbulance}
	low := Need{Severity: incident.SeverityLow, RequiredType: unit.UnitTypeAmbulance}

	closeCrit, _ := ScoreUnit(closeWrongType, critical)
	farCrit, _ := ScoreUnit(fartherRightType, critical)
	if closeCrit <= farCrit {
		t.Errorf("critical should favour the FASTER unit: close=%v far=%v", closeCrit, farCrit)
	}

	closeLow, _ := ScoreUnit(closeWrongType, low)
	farLow, _ := ScoreUnit(fartherRightType, low)
	if farLow <= closeLow {
		t.Errorf("low severity should favour the RIGHT type: close=%v far=%v", closeLow, farLow)
	}
}

// TestSeverityAloneCannotReorderIdenticalCandidates documents the property that
// motivated making severity a modulator: applied to candidates that differ in
// nothing, severity changes scores but never their ORDER.
func TestSeverityAloneCannotReorderIdenticalCandidates(t *testing.T) {
	a := mkUnit(unit.UnitTypeAmbulance, 1000)
	b := mkUnit(unit.UnitTypeAmbulance, 2000)

	for _, sev := range []string{
		incident.SeverityLow, incident.SeverityMedium,
		incident.SeverityHigh, incident.SeverityCritical,
	} {
		sa, _ := ScoreUnit(a, Need{Severity: sev})
		sb, _ := ScoreUnit(b, Need{Severity: sev})
		if sa <= sb {
			t.Errorf("severity %s: nearer unit must always outrank farther one", sev)
		}
	}
}

// TestSortDescendingIsStable guards the ranking contract the service relies on.
func TestSortDescendingIsStable(t *testing.T) {
	need := Need{Severity: incident.SeverityHigh, RequiredType: unit.UnitTypeAmbulance}
	units := []*unit.Unit{
		mkUnit(unit.UnitTypeFire, 500),
		mkUnit(unit.UnitTypeAmbulance, 900),
		mkUnit(unit.UnitTypeRescue, 4000),
	}

	scored := make([]ScoredUnit, 0, len(units))
	for _, u := range units {
		s, bd := ScoreUnit(u, need)
		scored = append(scored, ScoredUnit{Unit: *u, Score: s, Breakdown: bd})
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })

	for i := 1; i < len(scored); i++ {
		if scored[i-1].Score < scored[i].Score {
			t.Fatalf("not sorted descending at %d", i)
		}
	}
}

func mkShelter(dist float64, capacity, occupancy int) *shelter.Shelter {
	return &shelter.Shelter{
		Capacity: capacity, Occupancy: occupancy,
		Status: shelter.ShelterOpen, DistanceMeters: dist,
	}
}

// TestShelterHeadroomBreaksProximityTies is why capacity belongs in the shelter
// score: at equal distance, the emptier shelter must win, so load spreads instead
// of piling onto whichever shelter happens to be nearest.
func TestShelterHeadroomBreaksProximityTies(t *testing.T) {
	nearlyFull := mkShelter(1000, 100, 99) // 1 bed left
	roomy := mkShelter(1000, 100, 10)      // 90 beds left

	full, fullBD := ScoreShelter(nearlyFull)
	open, openBD := ScoreShelter(roomy)

	if open <= full {
		t.Errorf("roomier shelter should win at equal distance: roomy=%v full=%v", open, full)
	}
	if !almostEqual(fullBD.Proximity, openBD.Proximity) {
		t.Error("proximity should be identical; only headroom differs")
	}
	if fullBD.AvailableSpots != 1 || openBD.AvailableSpots != 90 {
		t.Errorf("available spots wrong: %d and %d", fullBD.AvailableSpots, openBD.AvailableSpots)
	}
}

// TestShelterProximityStillDominates: headroom breaks ties, it does not send
// survivors across the county to a slightly emptier shelter.
func TestShelterProximityStillDominates(t *testing.T) {
	nearAndTight := mkShelter(500, 100, 90) // close, 10% free
	farAndEmpty := mkShelter(20000, 100, 0) // 20km away, totally free

	near, _ := ScoreShelter(nearAndTight)
	far, _ := ScoreShelter(farAndEmpty)

	if near <= far {
		t.Errorf("a much closer shelter with room should still win: near=%v far=%v", near, far)
	}
}

func TestShelterScoreBounds(t *testing.T) {
	for _, sh := range []*shelter.Shelter{
		mkShelter(0, 10, 0), mkShelter(1e6, 10, 10), mkShelter(5000, 0, 0),
	} {
		s, _ := ScoreShelter(sh)
		if s < 0 || s > 1 {
			t.Errorf("score %v outside [0,1]", s)
		}
	}
}
