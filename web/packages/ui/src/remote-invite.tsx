import { useEffect, useState } from "react";
import { CAPABILITIES, type BrowserClientsView } from "@dark-factory/client";
import type { FactoryRemoteInvite } from "./factory-app-controller.js";
import { SectionHeader, formatTime } from "./console-kit.js";

export type RemoteInvitePanelProps = {
  invite?: FactoryRemoteInvite;
  error?: string;
  onInvite?: () => void;
  onDismiss?: () => void;
  /** The identities the factory has granted; asked for when the panel mounts. */
  devices?: BrowserClientsView;
  devicesError?: string;
  ownClientId?: string;
  onLoadDevices?: () => void;
  onRevokeDevice?: (device: { clientId: string; expectedRevision: bigint }) => void;
};

/** Pairing a phone from the console itself: one button, then the code it
 * mints. The link is shown as text, not an anchor: opening it here would pair
 * this desktop as a remote client and consume the phone's one-shot challenge.
 * Below it, every identity the factory has granted, with REVOKE for each
 * other than this console's own. */
export function RemoteInvitePanel({ invite, error, onInvite, onDismiss, devices, devicesError, ownClientId, onLoadDevices, onRevokeDevice }: RemoteInvitePanelProps) {
  const [confirming, setConfirming] = useState<string | undefined>(undefined);
  useEffect(() => { onLoadDevices?.(); }, []);
  return (
    <section className="dfFactoryConsole__section dfFactoryConsole__pairPhone" aria-label="Pair a phone">
      {invite === undefined ? (
        <button type="button" disabled={onInvite === undefined} onClick={onInvite}>Pair a phone</button>
      ) : (
        <>
          <img alt="QR code: scan with your phone" src={`data:image/svg+xml;utf8,${encodeURIComponent(invite.svg)}`} />
          <code>{invite.link}</code>
          <p>Valid for five minutes</p>
        </>
      )}
      {error === undefined ? null : (
        <p className="dfFactoryConsole__empty" role="alert">No pairing code — {error.replace(/_/g, " ")}</p>
      )}
      {invite === undefined && error === undefined ? null : (
        <button type="button" disabled={onDismiss === undefined} onClick={onDismiss}>Dismiss</button>
      )}
      <SectionHeader as="h4" title={devices?.more ? "Paired devices (first page)" : "Paired devices"} count={devices?.clients.length} />
      {devicesError === undefined ? null : (
        <p className="dfFactoryConsole__empty" role="alert">Devices — {devicesError.replace(/_/g, " ")}</p>
      )}
      {devices === undefined ? <p className="dfFactoryConsole__empty">asking the factory</p> : devices.clients.length === 0 ? <p className="dfFactoryConsole__empty">nothing paired</p> : (
        <ul className="dfFactoryConsole__devices">
          {devices.clients.map((device) => {
            const own = device.clientId === ownClientId;
            // A grant without terminal input is a phone; the loopback grant is a browser on this machine.
            const kind = (device.capabilities & CAPABILITIES.terminal_input) === 0 ? "Phone" : "Browser";
            return (
              <li key={device.clientId} className="dfFactoryConsole__device">
                <span>{kind}{own ? " · This browser" : ""}</span>
                <span className="dfFactoryConsole__deviceSince">{formatTime(device.createdAtMs)}</span>
                {own || onRevokeDevice === undefined ? null : confirming === device.clientId ? (
                  <>
                    <button type="button" className="dfDanger" onClick={() => { setConfirming(undefined); onRevokeDevice({ clientId: device.clientId, expectedRevision: device.revision }); }}>Confirm revoke</button>
                    <button type="button" onClick={() => setConfirming(undefined)}>Keep</button>
                  </>
                ) : (
                  <button type="button" className="dfDanger" onClick={() => setConfirming(device.clientId)}>Revoke</button>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
