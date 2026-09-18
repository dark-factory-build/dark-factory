"use client";

import { useEffect, useRef, useState, type ReactNode } from "react";
import {
  consumeInvitation,
  createRemoteManager,
  createRemoteStore,
  parseInvitation,
  type HumanRequestItem,
  type PushSubscribeBody,
  type RemoteInvitation,
  type RemoteManager,
  type RemoteManagerOptions,
  type RemoteStore,
  type StateView,
  type TaskItem,
} from "@dark-factory/client";
import { AgentStrip, StageMeter } from "../console-screens.js";
import { AnswerControls } from "../console-interactions.js";
import { HumanRequestFlow, type HumanRequestFlowSelection } from "../human-request-flow.js";
import {
  FACTORY_UNREACHABLE,
  INVITATION_SPENT,
  INVITATION_UNREADABLE,
  REMOTE_STATUS_GLYPH,
  REQUEST_CLOSED,
  remoteActionable,
  remoteDeliveryNotice,
  remoteFactoryBanner,
  remotePairFailure,
  remoteProjectGroups,
  shortRemoteID,
} from "./remote-view.js";

export type RemoteAppProps = {
  /** Durable bindings; the IndexedDB store when the host supplies none. */
  store?: RemoteStore;
  /** The seam a test drives the whole console through, without a relay. */
  managerFactory?: (options: RemoteManagerOptions) => RemoteManager;
  location?: Pick<Location, "hash" | "pathname" | "search" | "origin">;
  history?: Pick<History, "replaceState" | "state">;
  navigator?: Pick<Navigator, "onLine">;
  /** Turns this device's push subscription on; the browser implementation when the host supplies none. */
  subscribePush?: () => Promise<PushSubscribeBody>;
  /** Which iOS browser this is when the page is not yet on the Home Screen; read from the browser when the host supplies none. */
  install?: InstallHint;
};

type InstallHint = "safari" | "other" | undefined;

/**
 * Only iOS needs telling: it has no install prompt, its Home Screen app keeps
 * storage apart from every browser tab, and alerts work nowhere else.
 */
function browserInstallHint(): InstallHint {
  const scope = globalThis as { matchMedia?: (query: string) => { matches: boolean }; navigator?: { userAgent?: string; standalone?: boolean } };
  const agent = scope.navigator?.userAgent ?? "";
  if (scope.matchMedia === undefined || !/iPhone|iPad|iPod/.test(agent)) return undefined;
  if (scope.navigator?.standalone === true || scope.matchMedia("(display-mode: standalone)").matches) return undefined;
  return /CriOS|FxiOS|EdgiOS/.test(agent) ? "other" : "safari";
}

const ALERTS_NEED_INSTALL = "On iPhone, add this page to the Home Screen first: Share, then Add to Home Screen, and open Dark Factory from there.";
const ALERTS_REFUSED = "ALERTS WERE REFUSED BY THIS BROWSER. ALLOW NOTIFICATIONS FOR THIS SITE TO TURN THEM ON.";

/**
 * The browser half of alerts: permission, then one push subscription made with
 * a key pair this device mints. The device keeps the private key too, because
 * every factory it pairs with must be able to sign for the one subscription a
 * push service will give it.
 */
async function browserPushSubscription(): Promise<PushSubscribeBody> {
  if (!("PushManager" in globalThis) || globalThis.navigator?.serviceWorker === undefined) throw new Error(ALERTS_NEED_INSTALL);
  if (await Notification.requestPermission() !== "granted") throw new Error(ALERTS_REFUSED);
  const registration = await navigator.serviceWorker.ready;
  const stale = await registration.pushManager.getSubscription();
  if (stale !== null) await stale.unsubscribe();
  const pair = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, true, ["sign"]);
  const publicKey = new Uint8Array(await crypto.subtle.exportKey("raw", pair.publicKey));
  const privateKey = new Uint8Array(await crypto.subtle.exportKey("pkcs8", pair.privateKey));
  const subscription = await registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: publicKey });
  const encode = (bytes: Uint8Array) => btoa(String.fromCharCode(...bytes)).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
  return { endpoint: subscription.endpoint, public_key: encode(publicKey), private_key: encode(privateKey) };
}

type Pairing =
  | Readonly<{ phase: "idle" }>
  | Readonly<{ phase: "pairing" }>
  | Readonly<{ phase: "failed"; copy: string }>;

type RemoteScope = {
  nodeId: string;
  label: string;
};
type Detail = HumanRequestFlowSelection<RemoteScope>;

type Confirm = Readonly<{ kind: "factory"; nodeId: string }> | Readonly<{ kind: "device" }>;

const INVITATION_KEY = /(?:^|[?#&])df_remote(?:[&=]|$)/i;
const CANCEL_PHRASE = "CANCEL RUN";
const IDLE: Pairing = { phase: "idle" };

/**
 * The phone-shaped remote console. It owns one RemoteManager and renders the
 * factories that manager holds; every effect it offers is one-shot, and there
 * is no terminal here at all — a phone answers questions, it does not drive a
 * TUI.
 */
export function RemoteApp(props: RemoteAppProps = {}) {
  const [version, setVersion] = useState(0);
  const [pairing, setPairing] = useState<Pairing>(IDLE);
  const [pasting, setPasting] = useState(false);
  const [link, setLink] = useState("");
  const [name, setName] = useState<string | undefined>(undefined);
  const [detail, setDetailState] = useState<Detail | undefined>(undefined);
  const [confirm, setConfirm] = useState<Confirm | undefined>(undefined);
  const [cancelPhrase, setCancelPhrase] = useState<string | undefined>(undefined);
  const [alerts, setAlerts] = useState<{ phase: "idle" | "working" | "failed"; copy?: string }>({ phase: "idle" });
  const [online, setOnline] = useState(() => (props.navigator ?? globalThis.navigator)?.onLine !== false);
  const manager = useRef<RemoteManager | undefined>(undefined);
  // Read and cleared once per mount, not once per effect run.
  const arrival = useRef<{ attempted: boolean; link: string; invitation: RemoteInvitation | null } | undefined>(undefined);
  const [install] = useState<InstallHint>(() => "install" in props ? props.install : browserInstallHint());
  // An invitation that arrives in an iOS tab is held, not spent: pairing the
  // tab would leave the Home Screen app, which shares none of its storage, unpaired.
  const [held, setHeld] = useState<{ link: string; invitation: RemoteInvitation; copied: boolean } | undefined>(undefined);
  const bump = () => { if (manager.current !== undefined) setVersion((value) => value + 1); };
  const onlineRef = useRef(online);
  onlineRef.current = online;
  const human = useRef<HumanRequestFlow<RemoteScope> | undefined>(undefined);
  if (human.current === undefined) {
    const flow = new HumanRequestFlow<RemoteScope>({
      active: (scope) => remoteActionable(manager.current?.factories().find((factory) => factory.nodeId === scope.nodeId)?.status, onlineRef.current),
      currentRequest: (scope, requestID) => manager.current?.factories().find((factory) => factory.nodeId === scope.nodeId)?.state?.humanRequests.get(requestID),
      session: (scope) => manager.current?.client(scope.nodeId)?.session,
      onChange: () => { if (manager.current !== undefined) setDetailState(flow.selection); },
      afterAction: "refresh",
      replaceReady: true,
      unavailableNotice: FACTORY_UNREACHABLE,
      absentNotice: REQUEST_CLOSED,
      actionFailureNotice: remoteDeliveryNotice,
    });
    human.current = flow;
  }

  useEffect(() => {
    // A host that states the device's connectivity owns it; only when none is
    // given does this listen to the browser itself.
    const stated = props.navigator;
    if (stated !== undefined) { setOnline(stated.onLine !== false); return; }
    const update = () => setOnline(globalThis.navigator?.onLine !== false);
    update();
    globalThis.addEventListener("online", update);
    globalThis.addEventListener("offline", update);
    return () => {
      globalThis.removeEventListener("online", update);
      globalThis.removeEventListener("offline", update);
    };
  }, [props.navigator]);

  useEffect(() => {
    // Permission is read at render; a revocation made while the app was in
    // the background shows the moment it is brought back.
    const page = (globalThis as { document?: Pick<Document, "addEventListener" | "removeEventListener"> }).document;
    if (page?.addEventListener === undefined) return;
    page.addEventListener("visibilitychange", bump);
    return () => page.removeEventListener("visibilitychange", bump);
  }, []);

  useEffect(() => {
    const where = props.location ?? window.location;
    const past = props.history ?? window.history;
    const build = props.managerFactory ?? createRemoteManager;
    const built = build({
      store: props.store ?? createRemoteStore(),
      origin: where.origin,
      onChange: bump,
    });
    manager.current = built;
    bump();
    // Read-and-clear before anything can await: a one-shot invitation must not
    // survive in the address bar for a reload to replay. StrictMode runs this
    // effect twice, so the fragment is spent by the mount, not by a run that is
    // about to be thrown away.
    if (arrival.current === undefined) arrival.current = { attempted: invitationArrived(where.hash), link: `${where.origin}${where.pathname}${where.hash}`, invitation: consumeInvitation(where, past) };
    const { attempted, link: arrived, invitation } = arrival.current;
    void (async () => {
      try { await built.start(); } catch { /* an unreadable store is an empty device, not a crash */ }
      // Identity, never a shared flag: only the run that still owns the manager
      // may act on it.
      if (manager.current !== built) return;
      bump();
      if (invitation !== null && install !== undefined) setHeld({ link: arrived, invitation, copied: false });
      else if (invitation !== null) await pairWith(built, invitation);
      else if (attempted) setPairing({ phase: "failed", copy: INVITATION_SPENT });
    })();
    return () => {
      if (manager.current === built) manager.current = undefined;
      built.close();
    };
  }, []);

  const owner = manager.current;
  const factories = owner?.factories() ?? [];
  const needsYou = owner?.needsYou() ?? [];
  const selectedId = owner?.selected();
  const selected = factories.find((factory) => factory.nodeId === selectedId) ?? factories[0];
  const working = detail !== undefined && busy(detail);
  // A stored subscription is only alerts if the browser still lets this site
  // notify; a permission revoked in settings puts the button back.
  const alertsOn = owner?.push() !== undefined && ((globalThis as { Notification?: { permission?: string } }).Notification?.permission ?? "granted") === "granted";
  const byNode = new Map(factories.map((factory) => [factory.nodeId, factory] as const));
  const actionable = (nodeId: string) => remoteActionable(byNode.get(nodeId)?.status, online);

  useEffect(() => { human.current?.reconcile(); }, [version]);

  async function pairWith(target: RemoteManager, invitation: RemoteInvitation): Promise<void> {
    setPairing({ phase: "pairing" });
    try {
      await target.pair(invitation);
      if (manager.current !== target) return;
      setPairing(IDLE);
      setPasting(false);
      setLink("");
    } catch (error) {
      if (manager.current !== target) return;
      setPairing({ phase: "failed", copy: remotePairFailure(error) });
    }
    bump();
  }

  const pairPasted = () => {
    const target = manager.current;
    if (target === undefined || pairing.phase === "pairing") return;
    let invitation: RemoteInvitation;
    try { invitation = parseInvitation(link); } catch { setPairing({ phase: "failed", copy: INVITATION_UNREADABLE }); return; }
    void pairWith(target, invitation);
  };

  const select = (nodeId: string) => {
    try { manager.current?.select(nodeId); } catch { /* a binding can be forgotten between render and tap */ }
    bump();
  };

  const open = (nodeId: string, label: string, request: HumanRequestItem) => {
    if (!actionable(nodeId)) return;
    setCancelPhrase(undefined);
    human.current?.open({ nodeId, label }, request);
  };
  const reply = () => { setCancelPhrase(undefined); human.current?.reply(); };
  const changeReply = (value: string) => human.current?.setReply(value);
  const cancelRun = () => {
    if (cancelPhrase?.trim().toUpperCase() !== CANCEL_PHRASE) return;
    setCancelPhrase(undefined);
    human.current?.cancel();
  };

  const forget = (nodeId: string) => {
    setConfirm(undefined);
    if (detail?.scope.nodeId === nodeId) human.current?.clear(true);
    void (async () => {
      try { await manager.current?.forget(nodeId); } catch { /* the binding is gone either way */ }
      bump();
    })();
  };

  const enableAlerts = () => {
    const target = manager.current;
    if (target === undefined || alerts.phase === "working") return;
    setAlerts({ phase: "working" });
    void (async () => {
      try {
        const subscription = await (props.subscribePush ?? browserPushSubscription)();
        if (manager.current !== target) return;
        await target.setPush(subscription);
        setAlerts({ phase: "idle" });
      } catch (error) {
        if (manager.current !== target) return;
        setAlerts({ phase: "failed", copy: error instanceof Error && error.message.length > 0 ? error.message : ALERTS_REFUSED });
      }
      bump();
    })();
  };

  const forgetDevice = () => {
    setConfirm(undefined);
    human.current?.clear(true);
    void (async () => {
      try { await manager.current?.forgetDevice(); } catch { /* the bindings are gone either way */ }
      bump();
    })();
  };

  const installCard = install === undefined ? null : (
    <section className="dfFactoryConsole__section dfRemote__install" aria-label="Install the app">
      <div className="dfFactoryConsole__sectionHeading"><h2>INSTALL THE APP</h2></div>
      <p className="dfRemote__prose">
        {install === "safari"
          ? "Tap Share in the toolbar, then Add to Home Screen."
          : "Tap Share at the end of the address bar, then Add to Home Screen."}
        {" "}Open Dark Factory from the Home Screen: alerts only work there, and the app keeps its own pairing, apart from this tab.
      </p>
      {held === undefined ? null : (
        <p className="dfRemote__prose">
          Your invitation is waiting and lasts five minutes. Copy it, install, then paste it under SETUP in the app.
        </p>
      )}
      <div className="dfRemote__actions">
        {held === undefined ? null : (
          <button
            type="button"
            className="dfRemote__copyInvitation"
            onClick={() => { void globalThis.navigator.clipboard.writeText(held.link).then(() => setHeld({ ...held, copied: true }), () => { /* a refused clipboard leaves PAIR IN THIS TAB */ }); }}
          >
            {held.copied ? "COPIED" : "COPY INVITATION"}
          </button>
        )}
        {install === "safari" ? null : <a className="dfRemote__openSafari" href={`x-safari-${held?.link ?? "https://app.darkfactory.build/remote"}`}>OPEN IN SAFARI</a>}
        {held === undefined ? null : (
          <button
            type="button"
            className="dfRemote__pairHere"
            disabled={!online || pairing.phase === "pairing"}
            onClick={() => { const target = manager.current; setHeld(undefined); if (target !== undefined) void pairWith(target, held.invitation); }}
          >
            PAIR IN THIS TAB
          </button>
        )}
      </div>
    </section>
  );

  const setup = (
    <div id="dfRemoteSetup" className="dfRemote__setup">
        <section className="dfFactoryConsole__section dfRemote__pair" aria-label="Pair a factory">
          <div className="dfFactoryConsole__sectionHeading"><h2>PAIR A FACTORY</h2></div>
          {pairing.phase === "pairing" ? (
            <p className="dfRemote__pairing" role="status">PAIRING FACTORY…</p>
          ) : null}
          {pairing.phase === "failed" ? (
            <p className="dfRemote__pairError" role="alert">{pairing.copy}</p>
          ) : null}
          {pasting ? (
            <div className="dfRemote__paste">
              <label htmlFor="dfRemoteInvitation">INVITATION LINK</label>
              <textarea
                id="dfRemoteInvitation"
                className="dfRemote__link"
                value={link}
                disabled={!online || pairing.phase === "pairing"}
                onChange={(event) => setLink(event.currentTarget.value)}
              />
              <div className="dfRemote__actions">
                <button
                  type="button"
                  className="dfRemote__pairAction"
                  disabled={!online || pairing.phase === "pairing" || link.trim().length === 0}
                  onClick={pairPasted}
                >
                  PAIR
                </button>
                <button
                  type="button"
                  className="dfRemote__pasteCancel"
                  disabled={pairing.phase === "pairing"}
                  onClick={() => { setPasting(false); setLink(""); }}
                >
                  NOT NOW
                </button>
              </div>
            </div>
          ) : (
            <button
              type="button"
              className="dfRemote__pasteOpen"
              disabled={!online || pairing.phase === "pairing"}
              onClick={() => { setPasting(true); setPairing(IDLE); }}
            >
              PAIR A FACTORY
            </button>
          )}
        </section>

{factories.length === 0 ? (
          <section className="dfFactoryConsole__section dfRemote__none" aria-label="No factories">
            <div className="dfFactoryConsole__sectionHeading"><h2>NO FACTORY ON THIS DEVICE</h2></div>
            <p className="dfRemote__prose">
              On a paired desktop, open the factory console and choose PAIR A PHONE, then scan the
              code or open its link on this phone.
            </p>
            <p className="dfRemote__prose">
              You can also paste that link above. The link works once and only on the device that
              opens it.
            </p>
          </section>
        ) : null}
        {held !== undefined || factories.length === 0 ? null : installCard}
        {factories.length < 2 ? null : (
          <nav className="dfRemote__switcher" aria-label="Factories on this device">
            <ul className="dfRemote__factories">
              {factories.map((factory) => {
                const waiting = needsYou.filter((item) => item.nodeId === factory.nodeId).length;
                return (
                  <li key={factory.nodeId}>
                    <button
                      type="button"
                      className={`dfRemote__factory dfRemote__factory--${factory.status}${waiting > 0 ? " dfRemote__factory--waiting" : ""}`}
                      aria-pressed={factory.nodeId === selected?.nodeId}
                      aria-label={`${factory.label}: ${factory.status}, ${waiting} needs you`}
                      disabled={!online}
                      onClick={() => select(factory.nodeId)}
                    >
                      <span className="dfRemote__factoryGlyph" aria-hidden="true">{REMOTE_STATUS_GLYPH[factory.status]}</span>
                      <span className="dfRemote__factoryLabel">{factory.label}</span>
                      <span className="dfRemote__factoryStatus">{factory.status}</span>
                      <span className="dfRemote__factoryCount">{waiting} NEEDS YOU</span>
                    </button>
                  </li>
                );
              })}
            </ul>
          </nav>
        )}

        {factories.length === 0 ? null : (
          <section className="dfFactoryConsole__section dfRemote__alerts" aria-label="Alerts">
            <div className="dfFactoryConsole__sectionHeading">
              <h2>ALERTS</h2>
              <span>{alertsOn ? "ON" : "OFF"}</span>
            </div>
            {alerts.phase === "failed" ? <p className="dfRemote__pairError" role="alert">{alerts.copy}</p> : null}
            {!alertsOn ? (
              <>
                <p className="dfRemote__prose">Get a notification on this device when a factory needs you.</p>
                <button type="button" className="dfRemote__alertsOn" disabled={!online || alerts.phase === "working"} onClick={enableAlerts}>
                  {alerts.phase === "working" ? "TURNING ON…" : "ENABLE ALERTS"}
                </button>
              </>
            ) : (
              <p className="dfRemote__prose">This device is woken when any paired factory needs you.</p>
            )}
          </section>
        )}

        {selected === undefined ? null : (
          <div className="dfFactoryConsole__section dfRemote__rename">
            <label htmlFor="dfRemoteName">FACTORY NAME ON THIS DEVICE</label>
            <input id="dfRemoteName" className="dfRemote__nameText" value={name ?? selected.label} maxLength={32} onChange={(event) => setName(event.currentTarget.value)} />
            <button
              type="button"
              className="dfRemote__renameAction"
              disabled={name === undefined || name.trim().length === 0}
              onClick={() => { void manager.current?.rename(selected.nodeId, name ?? "").then(() => setName(undefined), () => { /* the old name stands */ }); }}
            >
              RENAME
            </button>
          </div>
        )}

        {selected === undefined ? null : (
          <ConfirmAction
            className="dfRemote__forgetFactory"
            label="FORGET THIS FACTORY"
            confirmLabel={`FORGET ${selected.label}`}
            open={confirm?.kind === "factory" && confirm.nodeId === selected.nodeId}
            disabled={false}
            onOpen={() => setConfirm({ kind: "factory", nodeId: selected.nodeId })}
            onKeep={() => setConfirm(undefined)}
            onConfirm={() => forget(selected.nodeId)}
          />
        )}
        {factories.length === 0 ? null : (
          <ConfirmAction
            className="dfRemote__forgetDevice"
            label="FORGET THIS DEVICE"
            confirmLabel="FORGET EVERYTHING"
            open={confirm?.kind === "device"}
            disabled={false}
            onOpen={() => setConfirm({ kind: "device" })}
            onKeep={() => setConfirm(undefined)}
            onConfirm={forgetDevice}
          />
        )}
    </div>
  );

  const banner = selected === undefined ? undefined : remoteFactoryBanner(selected.status);

  return (
    <div className="dfConsoleShell dfRemote">
      <main id="dfRemoteTop" className="dfFactoryConsole dfRemote__main" aria-label="Factory remote console">
        <nav className="dfRemote__bar" aria-label="Remote console">
          <a className="dfRemote__barName" href="#dfRemoteTop">
            <span aria-hidden="true">{selected === undefined ? "·" : REMOTE_STATUS_GLYPH[selected.status]}</span> {selected?.label ?? "DARK FACTORY"}
          </a>
          {needsYou.length === 0 ? null : <a className="dfRemote__barAlert" href="#dfRemoteTop">! {needsYou.length} NEEDS YOU</a>}
          <a href="#dfRemoteWork">WORK</a>
          <a href="#dfRemoteSetup">SETUP</a>
        </nav>
        {online ? null : (
          <p className="dfRemote__banner dfRemote__banner--device" role="status">DEVICE OFFLINE</p>
        )}
        {banner === undefined || selected === undefined ? null : (
          <p className={`dfRemote__banner dfRemote__banner--${selected.status === "offline" || selected.status === "connecting" ? "offline" : selected.status}`} role="status">
            {banner}
          </p>
        )}

        {held !== undefined || factories.length === 0 ? installCard : null}
        {factories.length === 0 ? setup : null}

        {factories.length === 0 ? null : (
          <section className="dfFactoryConsole__section dfRemote__needsYou" aria-label="NEEDS YOU">
            {needsYou.length === 0 ? (
              <p className="dfFactoryConsole__empty">all quiet — nothing needs you</p>
            ) : (
              <ul className="dfFactoryConsole__list">
                {needsYou.map((item) => (
                  <li className="dfFactoryConsole__card dfRemote__question" key={`${item.nodeId}:${item.request.id}`}>
                    <div className="dfFactoryConsole__cardTitle">
                      <strong>{entityName(byNode.get(item.nodeId)?.state?.agents, item.request.agent_id, "AGENT")} asks</strong>
                      <span className="dfRemote__tag">{item.label}</span>
                    </div>
                    <p>
                      {entityName(byNode.get(item.nodeId)?.state?.projects, item.request.project_id, "project")} · TASK {shortRemoteID(item.request.task_id)}
                    </p>
                    <button
                      type="button"
                      className="dfRemote__answer"
                      aria-pressed={detail !== undefined && detail.scope.nodeId === item.nodeId && detail.request.id === item.request.id}
                      disabled={!actionable(item.nodeId) || working}
                      onClick={() => open(item.nodeId, item.label, item.request)}
                    >
                      {item.request.can_reply ? "ANSWER" : "VIEW"}
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </section>
        )}

        {detail === undefined ? null : (
          <article className="dfFactoryConsole__section dfRemote__detail" role="dialog" aria-modal="true" aria-label="Selected question" aria-live="polite">
            <button
              type="button"
              className="dfRemote__close"
              disabled={busy(detail)}
              onClick={() => { setCancelPhrase(undefined); human.current?.clear(true); }}
            >
              CLOSE
            </button>
            <div className="dfFactoryConsole__sectionHeading">
              <h2>{detail.scope.label}</h2>
              <span>{detail.phase === "replying" ? "REPLYING" : detail.phase === "cancelling" ? "CANCELLING" : detail.phase.toUpperCase()}</span>
            </div>
            {detail.notice === undefined ? null : (
              <p className="dfRemote__notice" role="status">{detail.notice}</p>
            )}
            {detail.phase === "loading" ? <p className="dfFactoryConsole__empty">LOADING THE QUESTION…</p> : null}
            {detail.detail === undefined ? null : (
              <>
                <p className="dfRemote__questionText">{detail.detail.question}</p>
                <AnswerControls surface="remote" options={detail.detail.options} canReply={detail.detail.canReply} reply={detail.reply} replyMaxBytes={detail.detail.replyMaxBytes} busy={busy(detail)} disabled={!actionable(detail.scope.nodeId)} onReplyChange={changeReply} onReply={reply} submitLabel="REPLY" submittingLabel="REPLYING…" />
                {detail.detail.canReply ? null : <p className="dfFactoryConsole__empty">{detail.request.status === "open" ? "THIS OPEN DECISION IS READ-ONLY IN THIS VIEW." : `THIS DECISION IS ${detail.request.status.replaceAll("_", " ").toUpperCase()}.`}</p>}
                {detail.detail.cancelRun === null ? null : cancelPhrase === undefined ? (
                  <button
                    type="button"
                    className="dfRemote__cancelOpen"
                    disabled={busy(detail) || !actionable(detail.scope.nodeId)}
                    onClick={() => setCancelPhrase("")}
                  >
                    CANCEL RUN
                  </button>
                ) : (
                  <div className="dfRemote__cancel">
                    <label htmlFor="dfRemoteCancel">TYPE {CANCEL_PHRASE} TO STOP THIS RUN</label>
                    <input
                      id="dfRemoteCancel"
                      className="dfRemote__cancelText"
                      value={cancelPhrase}
                      disabled={busy(detail) || !actionable(detail.scope.nodeId)}
                      onChange={(event) => setCancelPhrase(event.currentTarget.value)}
                    />
                    <div className="dfRemote__actions">
                      <button
                        type="button"
                        className="dfRemote__cancelAction"
                        disabled={busy(detail) || !actionable(detail.scope.nodeId) || cancelPhrase.trim().toUpperCase() !== CANCEL_PHRASE}
                        onClick={cancelRun}
                      >
                        {detail.phase === "cancelling" ? "CANCELLING…" : CANCEL_PHRASE}
                      </button>
                      <button
                        type="button"
                        className="dfRemote__cancelKeep"
                        disabled={busy(detail)}
                        onClick={() => setCancelPhrase(undefined)}
                      >
                        KEEP RUNNING
                      </button>
                    </div>
                  </div>
                )}
              </>
            )}
          </article>
        )}

        {selected === undefined ? null : (
          <>
            <AgentStrip state={selected.state} />
            <ProjectsSection state={selected.state} />
          </>
        )}

        {factories.length === 0 ? null : setup}
      </main>
    </div>
  );
}

/** The client scrubs an encoded fragment too, so this reads the same forms. */
function invitationArrived(hash: string): boolean {
  let decoded = hash;
  try { decoded = decodeURIComponent(hash); } catch { /* a malformed attempt still matches its raw key */ }
  return INVITATION_KEY.test(hash) || INVITATION_KEY.test(decoded);
}

/** A read or a one-shot effect this console is already waiting on. */
function busy(detail: Detail): boolean {
  return detail.phase === "loading" || detail.phase === "replying" || detail.phase === "cancelling";
}

/** Live work first; what has finished folds away so it never buries it. */
function ProjectsSection({ state }: { state: StateView | undefined }) {
  const groups = state === undefined ? [] : remoteProjectGroups(state);
  const row = (task: TaskItem) => (
    <li key={task.id}>
      <div className="dfConsoleRow">
        <span className="dfConsoleRow__title">{task.title}</span>
        <span className="dfConsoleRow__agent">{task.assigned_agent_id === "" ? "any eligible worker" : entityName(state?.agents, task.assigned_agent_id, "agent")}</span>
        <StageMeter stage={task.status} />
      </div>
    </li>
  );
  return (
    <section id="dfRemoteWork" className="dfFactoryConsole__section dfRemote__projects" aria-label="Work">
      {state === undefined ? <p className="dfFactoryConsole__empty">waiting for the factory</p>
        : groups.length === 0 ? <p className="dfFactoryConsole__empty">no projects yet</p> : (
        <ul className="dfRemote__projectList">
          {groups.map((group) => {
            const live = group.tasks.filter((task) => LIVE.has(task.status));
            const finished = group.tasks.filter((task) => !LIVE.has(task.status));
            return (
              <li key={group.id} className="dfRemote__project">
                <div className="dfRemote__projectHeading">
                  <strong>{group.name}</strong>
                  <span>{live.length} live</span>
                </div>
                {live.length === 0 ? <p className="dfFactoryConsole__empty">nothing in flight</p> : <ul className="dfConsoleRows">{live.map(row)}</ul>}
                {finished.length === 0 ? null : (
                  <details className="dfRemote__finished">
                    <summary>{finished.length} finished</summary>
                    <ul className="dfConsoleRows">{finished.map(row)}</ul>
                  </details>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

const LIVE: ReadonlySet<string> = new Set(["running", "queued", "blocked"]);

/** Destructive actions are two taps and never a browser dialog. */
function ConfirmAction({
  className,
  label,
  confirmLabel,
  open,
  disabled,
  onOpen,
  onKeep,
  onConfirm,
}: {
  className: string;
  label: string;
  confirmLabel: string;
  open: boolean;
  disabled: boolean;
  onOpen: () => void;
  onKeep: () => void;
  onConfirm: () => void;
}): ReactNode {
  if (!open) {
    return (
      <button type="button" className={className} disabled={disabled} onClick={onOpen}>
        {label}
      </button>
    );
  }
  return (
    <div className="dfRemote__actions">
      <button type="button" className={`${className} ${className}--confirm`} onClick={onConfirm}>
        {confirmLabel}
      </button>
      <button type="button" className="dfRemote__keep" onClick={onKeep}>KEEP</button>
    </div>
  );
}

function entityName(
  entities: ReadonlyMap<string, { name?: string; title?: string }> | undefined,
  id: string,
  fallback: string,
): string {
  const entity = entities?.get(id);
  return entity?.name ?? entity?.title ?? `${fallback} ${shortRemoteID(id)}`;
}
