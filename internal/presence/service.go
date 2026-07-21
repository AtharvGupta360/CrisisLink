package presence

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/common"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/geo"
)

const (
	// keyPrefix namespaces presence keys. This Redis instance is shared with the
	// rate limiter ("ratelimit:") and the shelter cache ("shelter:v1:"), so prefix
	// discipline is what keeps three unrelated concerns from colliding.
	keyPrefix = "presence:unit:"

	// HeartbeatInterval is the cadence units are expected to ping at. Exported so
	// the handler can advertise it to clients — the server, not the client, owns
	// what "alive" means.
	HeartbeatInterval = 10 * time.Second

	// TTL is 3x the interval, so a unit must miss TWO consecutive heartbeats before
	// it is declared dark. One dropped packet on a flaky mobile network must not
	// take a rescue unit out of service.
	//
	// The dial cuts both ways: a longer TTL tolerates more packet loss but makes us
	// SLOWER to notice a genuine death. 3x is the usual compromise between false
	// positives and detection latency.
	TTL = 3 * HeartbeatInterval

	// geoKey is the sorted set holding every unit's last reported position, indexed
	// by geohash so Redis can answer "who is near here" as a range scan.
	//
	// IMPORTANT ASYMMETRY: sorted-set MEMBERS cannot carry their own TTL. The
	// per-unit presence keys expire themselves; entries in this set do NOT. So this
	// set is an INDEX, not a source of truth — it will contain units that have long
	// gone dark, still sitting at their last position. Liveness is always decided by
	// the presence keys, and NearbyLive filters against them on every read (and
	// lazily evicts what it finds stale).
	geoKey = "presence:geo"
)

// Service is the presence API over Redis. It holds no Postgres handle at all —
// nothing here is durable, and that is the point.
type Service struct {
	rdb *redis.Client
}

func NewService(rdb *redis.Client) *Service {
	return &Service{rdb: rdb}
}

func key(unitID string) string { return keyPrefix + unitID }

// Heartbeat records a unit as alive at (lat,lng) and RESETS its expiry.
//
// This is the only write path, and it is deliberately cheap: one Redis SET, no
// Postgres round trip. We do NOT verify the unit exists in the database first —
// that would put a query on the hottest path in the system and reintroduce exactly
// the load we moved to Redis to avoid. The JWT already proves the caller is
// authenticated; a bogus id costs us one key that expires on its own in 30s.
//
// The lat/lng is not decoration: a heartbeat IS a position report. That is what
// makes live tracking (Redis GEO) a drop-in extension rather than a second endpoint
// clients have to call.
func (s *Service) Heartbeat(ctx context.Context, unitID string, lat, lng float64) error {
	if err := geo.ValidateLatLng(lat, lng); err != nil {
		return err
	}
	raw, err := json.Marshal(stored{Lat: lat, Lng: lng, TS: time.Now().Unix()})
	if err != nil {
		return err
	}

	// Two writes, ONE round trip. The pipeline is not for atomicity (these are
	// independent and a partial apply is harmless) — it is to keep the hottest
	// endpoint in the system at a single network hop.
	//
	//   SET  ... EX TTL  -> liveness, expires itself
	//   GEOADD           -> spatial index, does NOT expire (see geoKey)
	//
	// SET carries its expiry in the same command rather than a separate EXPIRE, so
	// there is never a window where the key exists without a lifetime.
	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, key(unitID), raw, TTL)
	pipe.GeoAdd(ctx, geoKey, &redis.GeoLocation{
		Name:      unitID,
		Longitude: lng, // longitude FIRST — the classic geospatial footgun
		Latitude:  lat,
	})
	_, err = pipe.Exec(ctx)
	return err
}

// NearbyUnit is one live unit found near a point, with its CURRENT distance.
type NearbyUnit struct {
	UnitID         string  `json:"unitId"`
	Latitude       float64 `json:"latitude"`
	Longitude      float64 `json:"longitude"`
	DistanceMeters float64 `json:"distanceMeters"`
}

// NearbyLive answers "which units are physically near this point RIGHT NOW",
// nearest first, using live heartbeat positions rather than registration pins.
//
// Two stages, and the second is the point:
//
//  1. GEOSEARCH the index for candidates within the radius.
//  2. Cross-check each against its presence key and DROP the dark ones — because
//     the index keeps members forever, it will confidently return units that
//     stopped reporting hours ago. The TTL keys are the authority on liveness.
//
// Dark members found in step 2 are lazily ZREM'd. Without that the set grows
// without bound as units churn; with it, cleanup is paid for by the reads that
// actually notice the garbage, and needs no reaper job.
//
// It over-fetches (2x limit) so that dropping dark members does not leave us short
// of the caller's requested count.
func (s *Service) NearbyLive(ctx context.Context, lat, lng, radiusMeters float64, limit int) ([]NearbyUnit, error) {
	if err := geo.ValidateLatLng(lat, lng); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 5
	}

	locs, err := s.rdb.GeoSearchLocation(ctx, geoKey, &redis.GeoSearchLocationQuery{
		GeoSearchQuery: redis.GeoSearchQuery{
			Longitude:  lng,
			Latitude:   lat,
			Radius:     radiusMeters,
			RadiusUnit: "m",
			Sort:       "ASC", // nearest first
			Count:      limit * 2,
		},
		WithCoord: true,
		WithDist:  true,
	}).Result()
	if err != nil {
		return nil, err
	}
	if len(locs) == 0 {
		return []NearbyUnit{}, nil
	}

	ids := make([]string, len(locs))
	for i, l := range locs {
		ids[i] = l.Name
	}
	live := s.FilterPresent(ctx, ids)

	out := make([]NearbyUnit, 0, limit)
	var stale []string
	for _, l := range locs {
		if !live[l.Name] {
			stale = append(stale, l.Name)
			continue
		}
		if len(out) < limit {
			out = append(out, NearbyUnit{
				UnitID:         l.Name,
				Latitude:       l.Latitude,
				Longitude:      l.Longitude,
				DistanceMeters: l.Dist, // metres, because RadiusUnit was "m"
			})
		}
	}
	if len(stale) > 0 {
		// Best-effort: a failed cleanup only means we re-notice them next time.
		if err := s.rdb.ZRem(ctx, geoKey, toAny(stale)...).Err(); err != nil {
			common.Logger.Warnw("could not evict dark units from geo index", "error", err)
		}
	}
	return out, nil
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// Get returns a unit's last known presence. found=false means the key expired (or
// never existed) — the unit has gone dark.
//
// Unlike the decision helpers below, a Redis failure here is returned to the
// caller. This backs an endpoint where someone explicitly ASKED about presence, and
// answering "present" when we genuinely do not know would be a lie to a human.
func (s *Service) Get(ctx context.Context, unitID string) (*Presence, bool, error) {
	raw, err := s.rdb.Get(ctx, key(unitID)).Bytes()
	if err == redis.Nil {
		return nil, false, nil // gone dark — an expected state, not an error
	}
	if err != nil {
		return nil, false, err
	}
	var st stored
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, false, err
	}
	seen := time.Unix(st.TS, 0)
	return &Presence{
		UnitID:     unitID,
		Latitude:   st.Lat,
		Longitude:  st.Lng,
		LastSeen:   seen,
		AgeSeconds: time.Since(seen).Seconds(),
	}, true, nil
}

// IsPresent reports whether a unit is currently alive.
//
// It FAILS OPEN: if Redis is unreachable we answer "present". That asymmetry is
// deliberate. Failing closed would mean NO unit is considered live during a Redis
// blip, so dispatch would refuse to assign anyone — a cache outage escalating into a
// total dispatch outage, in a disaster-response system. Wrongly dispatching to a
// unit that has gone dark is recoverable (the dispatch can be reassigned); refusing
// to dispatch at all is not.
//
// Compare the rate limiter (also fails open — a broken limiter must not be an
// outage) with an auth check, which must fail CLOSED. Knowing which you are
// building is the whole skill.
func (s *Service) IsPresent(ctx context.Context, unitID string) bool {
	n, err := s.rdb.Exists(ctx, key(unitID)).Result()
	if err != nil {
		common.Logger.Warnw("presence check failed, assuming present (fail-open)",
			"unitId", unitID, "error", err)
		return true
	}
	return n > 0
}

// FilterPresent answers "of these units, which are alive?" in ONE round trip.
//
// Dispatch needs this for a whole KNN candidate list, and doing N sequential EXISTS
// calls would add N network round trips to every dispatch decision. A pipeline
// sends them as one batch. Same fail-open policy as IsPresent, for the same reason.
func (s *Service) FilterPresent(ctx context.Context, unitIDs []string) map[string]bool {
	out := make(map[string]bool, len(unitIDs))
	if len(unitIDs) == 0 {
		return out
	}

	pipe := s.rdb.Pipeline()
	cmds := make([]*redis.IntCmd, len(unitIDs))
	for i, id := range unitIDs {
		cmds[i] = pipe.Exists(ctx, key(id))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		common.Logger.Warnw("batch presence check failed, assuming all present (fail-open)",
			"count", len(unitIDs), "error", err)
		for _, id := range unitIDs {
			out[id] = true
		}
		return out
	}
	for i, id := range unitIDs {
		n, err := cmds[i].Result()
		out[id] = err != nil || n > 0 // per-key failure also fails open
	}
	return out
}
