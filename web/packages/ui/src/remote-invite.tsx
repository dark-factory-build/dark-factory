import type { FactoryRemoteInvite } from "./factory-app-controller.js";

export type RemoteInvitePanelProps = {
  invite?: FactoryRemoteInvite;
  error?: string;
  onInvite?: () => void;
  onDismiss?: () => void;
};

/** Pairing a phone from the console itself: one button, then the code it mints. */
export function RemoteInvitePanel({ invite, error, onInvite, onDismiss }: RemoteInvitePanelProps) {
  return (
    <section className="dfFactoryConsole__section dfFactoryConsole__pairPhone" aria-label="PAIR A PHONE">
      {invite === undefined ? (
        <button type="button" disabled={onInvite === undefined} onClick={onInvite}>PAIR A PHONE</button>
      ) : (
        <>
          <img alt="QR code: scan with your phone" src={`data:image/svg+xml;utf8,${encodeURIComponent(invite.svg)}`} />
          <a href={invite.link}>{invite.link}</a>
          <p>EXPIRES {new Date(Number(invite.expiresAtMs)).toISOString()}</p>
          <button type="button" disabled={onDismiss === undefined} onClick={onDismiss}>DISMISS</button>
        </>
      )}
      {error === undefined ? null : (
        <p className="dfFactoryConsole__empty" role="alert">NO PAIRING CODE — {error.replace(/_/g, " ").toUpperCase()}</p>
      )}
    </section>
  );
}
