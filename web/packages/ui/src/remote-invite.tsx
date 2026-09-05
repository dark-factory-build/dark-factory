import type { FactoryRemoteInvite } from "./factory-app-controller.js";

export type RemoteInvitePanelProps = {
  invite?: FactoryRemoteInvite;
  error?: string;
  onInvite?: () => void;
  onDismiss?: () => void;
};

/** Pairing a phone from the console itself: one button, then the code it
 * mints. The link is shown as text, not an anchor: opening it here would pair
 * this desktop as a remote client and consume the phone's one-shot challenge. */
export function RemoteInvitePanel({ invite, error, onInvite, onDismiss }: RemoteInvitePanelProps) {
  return (
    <section className="dfFactoryConsole__section dfFactoryConsole__pairPhone" aria-label="PAIR A PHONE">
      {invite === undefined ? (
        <button type="button" disabled={onInvite === undefined} onClick={onInvite}>PAIR A PHONE</button>
      ) : (
        <>
          <img alt="QR code: scan with your phone" src={`data:image/svg+xml;utf8,${encodeURIComponent(invite.svg)}`} />
          <code>{invite.link}</code>
          <p>VALID FOR FIVE MINUTES</p>
        </>
      )}
      {error === undefined ? null : (
        <p className="dfFactoryConsole__empty" role="alert">NO PAIRING CODE — {error.replace(/_/g, " ").toUpperCase()}</p>
      )}
      {invite === undefined && error === undefined ? null : (
        <button type="button" disabled={onDismiss === undefined} onClick={onDismiss}>DISMISS</button>
      )}
    </section>
  );
}
