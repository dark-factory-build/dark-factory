import { useEffect, useRef, useState } from "react";
import { FactoryAppController, DEFAULT_BROWSER_URL, type FactoryAppSnapshot } from "./factory-app-controller.js";
import { FactoryConsole } from "./factory-console.js";

export type FactoryAppProps = {
  browserURL?: string;
};

const INITIAL_SNAPSHOT: FactoryAppSnapshot = { status: "idle" };

/** Complete browser application lifecycle; hosts only render this component. */
export function FactoryApp({ browserURL = DEFAULT_BROWSER_URL }: FactoryAppProps) {
  const [snapshot, setSnapshot] = useState<FactoryAppSnapshot>(INITIAL_SNAPSHOT);
  const owner = useRef<FactoryAppController | undefined>(undefined);

  useEffect(() => {
    const controller = new FactoryAppController({
      url: browserURL,
      origin: window.location.origin,
      location: window.location,
      history: window.history,
      onChange: setSnapshot,
    });
    owner.current = controller;
    controller.start();
    return () => {
      if (owner.current === controller) owner.current = undefined;
      controller.close();
    };
  }, [browserURL]);

  return (
    <FactoryConsole
      {...snapshot}
      onRetry={() => owner.current?.retry()}
      onSelectHumanRequest={(request) => { void owner.current?.selectHumanRequest(request); }}
      onHumanReplyChange={(reply) => owner.current?.setHumanReply(reply)}
      onReplyHumanRequest={() => { void owner.current?.replyHumanRequest(); }}
      onCancelHumanRequest={() => { void owner.current?.cancelHumanRequest(); }}
      onCloseHumanRequest={() => owner.current?.clearHumanRequest()}
    />
  );
}
