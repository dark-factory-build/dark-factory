"use client";

import { useEffect, useRef, useState } from "react";
import { FactoryAppController, type FactoryAppSnapshot } from "./factory-app-controller.js";
import { FactoryConsole } from "./factory-console.js";

const INITIAL_SNAPSHOT: FactoryAppSnapshot = { status: "idle" };

/** Complete browser application lifecycle; hosts only render this component. */
export function FactoryApp() {
  const [snapshot, setSnapshot] = useState<FactoryAppSnapshot>(INITIAL_SNAPSHOT);
  const owner = useRef<FactoryAppController | undefined>(undefined);

  useEffect(() => {
    const controller = new FactoryAppController({
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
  }, []);

  return (
    <FactoryConsole
      {...snapshot}
      onSelectHumanRequest={(request) => { void owner.current?.selectHumanRequest(request); }}
      onHumanReplyChange={(reply) => owner.current?.setHumanReply(reply)}
      onReplyHumanRequest={() => { void owner.current?.replyHumanRequest(); }}
      onCancelHumanRequest={() => { void owner.current?.cancelHumanRequest(); }}
      onCloseHumanRequest={() => owner.current?.clearHumanRequest()}
    />
  );
}
