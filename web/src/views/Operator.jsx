import { useEffect, useState, useCallback, useRef } from 'react';
import { api, fmtMeters, fmtEta, fmtAgo, POLL_MS } from '../api.js';
import MapView from '../components/MapView.jsx';
import { TopBar, useToast } from '../App.jsx';

const ACTIVE = ['reported', 'verified', 'dispatched'];
const SEV_RANK = { critical: 4, high: 3, medium: 2, low: 1 };

export default function Operator({ claims, onLogout }) {
  const toast = useToast();
  const [incidents, setIncidents] = useState([]);
  const [units, setUnits] = useState([]);
  const [selected, setSelected] = useState(null);
  const [cands, setCands] = useState(null);
  const [loadingCands, setLoadingCands] = useState(false);
  const [preempt, setPreempt] = useState(null);
  const [lag, setLag] = useState(0);
  const [racing, setRacing] = useState(false);
  const selectedRef = useRef(null);
  selectedRef.current = selected;

  // --- data loading ----------------------------------------------------------
  const loadIncidents = useCallback(async () => {
    try {
      const rows = await api.listIncidents(60);
      const active = rows
        .filter((i) => ACTIVE.includes(i.status))
        .sort(
          (a, b) =>
            (SEV_RANK[b.severity] || 0) - (SEV_RANK[a.severity] || 0) ||
            new Date(a.createdAt) - new Date(b.createdAt)
        );
      setIncidents(active);

      // Keep the selection consistent with reality. An incident that was resolved
      // (or preempted back to 'verified') must not linger in the right rail
      // showing a stale severity and a ranking nobody can act on. Re-point at the
      // fresh object so status/reportCount stay live; clear it if it left the queue.
      const sel = selectedRef.current;
      if (sel) {
        const fresh = active.find((i) => i.id === sel.id);
        if (!fresh) {
          setSelected(null);
          setCands(null);
        } else if (fresh.status !== sel.status || fresh.reportCount !== sel.reportCount) {
          setSelected(fresh);
        }
      }
      return active;
    } catch (e) {
      toast(e.message, 'err');
      return [];
    }
  }, [toast]);

  // Units come from the REGISTRY, then live presence is layered on top: any unit
  // with a live Redis key gets its live coordinates and is flagged isLive. The
  // rest stay at their registered position with a dashed outline. That contrast
  // — live vs registered — is the whole point of the presence subsystem.
  const loadUnits = useCallback(async () => {
    try {
      const registry = await api.listUnits();
      let live = [];
      try {
        const r = await api.liveNearby(28.6139, 77.209, 200000);
        live = r.units || [];
      } catch {
        /* Redis down: fall back to registered positions, exactly like the API does */
      }
      const liveById = new Map(live.map((u) => [u.unitId || u.id, u]));
      setUnits(
        registry.map((u) => {
          const l = liveById.get(u.id);
          return l
            ? { ...u, latitude: l.latitude, longitude: l.longitude, isLive: true }
            : { ...u, isLive: false };
        })
      );
    } catch (e) {
      toast(e.message, 'err');
    }
  }, [toast]);

  const loadLag = useCallback(async () => {
    try {
      const rows = await api.outbox();
      setLag(Array.isArray(rows) ? rows.length : 0);
    } catch {
      /* operators without admin rights simply don't see lag */
    }
  }, []);

  // `quiet` is used by the 4s poll: it refreshes the ranking IN PLACE. Blanking
  // the panel on every poll made the whole right rail flash twice a second and
  // made the Dispatch buttons unclickable as they were torn down and rebuilt.
  // Only an explicit selection clears the old data.
  const loadCandidates = useCallback(
    async (incidentId, quiet = false) => {
      if (!quiet) {
        setLoadingCands(true);
        setCands(null);
      }
      try {
        const fresh = await api.candidates(incidentId);
        // Discard a slow response for an incident the operator has since moved
        // off — otherwise a laggy request overwrites the current selection.
        if (selectedRef.current?.id === incidentId) setCands(fresh);
      } catch (e) {
        if (!quiet) toast(e.message, 'err');
      } finally {
        if (!quiet) setLoadingCands(false);
      }
    },
    [toast]
  );

  useEffect(() => {
    loadIncidents();
    loadUnits();
    loadLag();
  }, [loadIncidents, loadUnits, loadLag]);

  // Poll for live movement. 4s is a compromise: fast enough that a unit visibly
  // moves while you watch, slow enough not to hammer the API during a demo.
  useEffect(() => {
    const t = setInterval(() => {
      loadUnits();
      loadLag();
      if (selectedRef.current) loadCandidates(selectedRef.current.id, true); // quiet
    }, POLL_MS);
    return () => clearInterval(t);
  }, [loadUnits, loadLag, loadCandidates]);

  function select(inc) {
    setSelected(inc);
    loadCandidates(inc.id);
  }

  // --- actions ---------------------------------------------------------------
  async function doDispatch(unitId, callSign) {
    try {
      await api.dispatch(selected.id, unitId);
      toast(`Dispatched ${callSign}`, 'ok');
      await Promise.all([loadIncidents(), loadUnits()]);
      loadCandidates(selected.id);
    } catch (e) {
      // 409 is the system correctly refusing, not a failure of the UI.
      toast(e.status === 409 ? `${callSign} was just taken (409)` : e.message, 'err');
      loadCandidates(selected.id);
    }
  }

  // THE CONCURRENCY DEMO. Ten dispatch requests for the SAME unit, fired
  // simultaneously. Exactly one 201 and nine 409s is the no-double-booking
  // invariant, made clickable.
  async function fireRace() {
    if (!cands?.candidates?.length) return toast('No candidate to race for', 'err');
    const target = cands.candidates[0];
    setRacing(true);
    const results = await Promise.allSettled(
      Array.from({ length: 10 }, () => api.dispatch(selected.id, target.unit.id))
    );
    const won = results.filter((r) => r.status === 'fulfilled').length;
    const lost = results.length - won;
    toast(`${target.unit.callSign}: ${won} won (201), ${lost} refused (409)`, won === 1 ? 'ok' : 'err');
    setRacing(false);
    await Promise.all([loadIncidents(), loadUnits()]);
    loadCandidates(selected.id);
  }

  async function openPreempt() {
    try {
      setPreempt(await api.preemptable(selected.id));
    } catch (e) {
      toast(e.message, 'err');
    }
  }

  async function doPreempt() {
    try {
      await api.preempt(selected.id);
      toast('Unit preempted from a lower-severity incident', 'ok');
      setPreempt(null);
      await Promise.all([loadIncidents(), loadUnits()]);
      loadCandidates(selected.id);
    } catch (e) {
      toast(e.message, 'err');
    }
  }

  const liveCount = units.filter((u) => u.isLive).length;

  return (
    <div className="app">
      <TopBar
        claims={claims}
        onLogout={onLogout}
        extra={
          <>
            <span className="stat">
              <i className={`dot ${liveCount ? 'ok' : 'bad'}`} />
              <b>{liveCount}</b> units live
            </span>
            <span className="stat">
              <i className={`dot ${lag === 0 ? 'ok' : lag < 20 ? 'warn' : 'bad'}`} />
              outbox lag <b>{lag}</b>
            </span>
          </>
        }
      />

      <div className="body">
        {/* ---- incident queue ---- */}
        <div className="rail">
          <div className="rail-head">Incident queue · {incidents.length}</div>
          {incidents.length === 0 && <div className="empty">No active incidents</div>}
          {incidents.map((i) => (
            <div
              key={i.id}
              className={`row ${selected?.id === i.id ? 'selected' : ''}`}
              onClick={() => select(i)}
            >
              <div className="row-title">{i.title}</div>
              <div className="row-meta">
                <span className={`sev ${i.severity}`}>{i.severity}</span>
                <span className="pill">{i.status}</span>
                {i.reportCount > 1 && <span className="pill count">×{i.reportCount}</span>}
                <span className="time">{fmtAgo(i.createdAt)}</span>
              </div>
            </div>
          ))}
        </div>

        {/* ---- map ---- */}
        <div className="map-wrap">
          <MapView
            incidents={incidents}
            units={units}
            selectedId={selected?.id}
            onSelectIncident={select}
            flyTo={selected ? [selected.latitude, selected.longitude] : null}
          />
        </div>

        {/* ---- candidates ---- */}
        <div className="rail right">
          <div className="rail-head">Dispatch candidates</div>

          {!selected && <div className="empty">Select an incident to rank units</div>}
          {selected && loadingCands && !cands && <div className="empty">Scoring...</div>}

          {selected && cands && (
            <>
              <div className="weights">
                severity <b>{selected.severity}</b> → weights{' '}
                <b>{Math.round((cands.candidates?.[0]?.breakdown?.weights?.time ?? 0) * 100)}%</b> time ·{' '}
                <b>{Math.round((cands.candidates?.[0]?.breakdown?.weights?.skill ?? 0) * 100)}%</b> skill
                <div style={{ marginTop: 7 }} className="legend">
                  <span><i style={{ background: 'var(--accent)' }} />time</span>
                  <span><i style={{ background: '#6d5bd0' }} />skill</span>
                  <span>source: {cands.positionSource || '-'}</span>
                </div>
              </div>

              {(cands.candidates || []).length === 0 && (
                <div className="empty">
                  No units available.
                  <br />
                  <button style={{ marginTop: 10 }} onClick={openPreempt}>
                    See what could be preempted
                  </button>
                </div>
              )}

              {(cands.candidates || []).map((c, idx) => {
                const b = c.breakdown || {};
                const w = b.weights || {};
                // The two segments are the WEIGHTED contributions, so their combined
                // width literally is the score. The ranking is not a black box.
                const timePart = (b.timeScore ?? 0) * (w.time ?? 0);
                const skillPart = (b.skillScore ?? 0) * (w.skill ?? 0);
                return (
                  <div className="cand" key={c.unit.id}>
                    <div className="cand-head">
                      <span className="cand-name">
                        <span className="rank">{idx + 1}.</span>
                        {c.unit.callSign}
                      </span>
                      <span className="cand-score">{c.score.toFixed(2)}</span>
                    </div>

                    <div className="bar">
                      <div className="seg-time" style={{ width: `${timePart * 100}%` }} />
                      <div className="seg-skill" style={{ width: `${skillPart * 100}%` }} />
                    </div>

                    <div className="cand-detail">
                      <span><span className="k">time</span> {(b.timeScore ?? 0).toFixed(2)}</span>
                      <span><span className="k">skill</span> {(b.skillScore ?? 0).toFixed(2)}</span>
                      <span><span className="k">dist</span> {fmtMeters(b.distanceMeters)}</span>
                      <span><span className="k">ETA</span> {fmtEta(b.etaSeconds)}</span>
                      <span className="dim">{c.unit.type}</span>
                    </div>

                    <button className="primary" onClick={() => doDispatch(c.unit.id, c.unit.callSign)}>
                      Dispatch
                    </button>
                  </div>
                );
              })}
            </>
          )}
        </div>
      </div>

      {/* ---- demo toolbar ---- */}
      <div className="toolbar">
        <button onClick={fireRace} disabled={!cands?.candidates?.length || racing}>
          {racing ? 'Racing...' : '⚡ Fire 10 concurrent dispatches'}
        </button>
        <button onClick={openPreempt} disabled={!selected}>👁 Preemptable</button>
        <button onClick={() => { loadIncidents(); loadUnits(); }}>↻ Refresh</button>
        <span className="stat dim" style={{ marginLeft: 'auto' }}>
          {units.length} units registered · {liveCount} broadcasting
        </span>
      </div>

      {preempt && (
        <div className="modal-back" onClick={() => setPreempt(null)}>
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <h3>Preemptable units</h3>
            <p className="sub dim" style={{ fontSize: 12 }}>
              Taking a unit means someone else stops receiving help. Only incidents of
              strictly lower severity can be preempted, so two equal incidents can never
              take a unit back and forth.
            </p>
            {(preempt.preemptable || []).length === 0 ? (
              <div className="empty">Nothing less severe is currently assigned.</div>
            ) : (
              <table>
                <thead>
                  <tr><th>Unit</th><th>Currently on</th><th>Distance</th></tr>
                </thead>
                <tbody>
                  {preempt.preemptable.map((p) => (
                    <tr key={p.dispatchId}>
                      <td><b>{p.callSign}</b></td>
                      <td><span className={`sev ${p.severity}`}>{p.severity}</span></td>
                      <td className="mono">{fmtMeters(p.distanceMeters)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
            <div style={{ display: 'flex', gap: 8, marginTop: 16 }}>
              <button
                className="primary"
                disabled={!(preempt.preemptable || []).length}
                onClick={doPreempt}
              >
                Preempt nearest
              </button>
              <button onClick={() => setPreempt(null)}>Cancel</button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
