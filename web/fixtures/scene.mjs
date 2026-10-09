// Small builders for the floor's world model (SceneGraph): units, machines and their readings.
export const unread = { evidence: "static", observation: "unobserved", state: "unknown", ratePerHour: 0, errorPermille: 0, latencyMs: 0 };
export const busy = { evidence: "both", observation: "observed", state: "active", ratePerHour: 120, errorPermille: 0, latencyMs: 40 };
export const machine = (id, kind, extra = {}) => ({ id, kind, label: id, reading: unread, ...extra });
export const unit = (id, extra = {}) => ({ id, label: id, reading: unread, machines: [], ...extra });
export const sceneGraph = (units, extra = {}) => ({ digest: units.map((item) => item.id).join(","), units, shared: [], parties: [], quarantine: [], flows: [], ...extra });
export const unitsOf = (ids, extra) => sceneGraph(ids.map((id) => unit(id)), extra);
