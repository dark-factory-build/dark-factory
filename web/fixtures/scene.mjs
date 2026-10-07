// Small builders for the floor's world model (SceneGraph): halls, machines and their readings.
export const unread = { evidence: "static", observation: "unobserved", state: "unknown", ratePerHour: 0, errorPermille: 0, latencyMs: 0 };
export const busy = { evidence: "both", observation: "observed", state: "active", ratePerHour: 120, errorPermille: 0, latencyMs: 40 };
export const machine = (id, kind, extra = {}) => ({ id, kind, label: id, reading: unread, ...extra });
export const hall = (id, extra = {}) => ({ id, label: id, band: 2, reading: unread, machines: [], ...extra });
export const sceneGraph = (halls, extra = {}) => ({ digest: halls.map((item) => item.id).join(","), halls, shared: [], parties: [], quarantine: [], flows: [], ...extra });
export const hallsOf = (ids, extra) => sceneGraph(ids.map((id) => hall(id)), extra);
