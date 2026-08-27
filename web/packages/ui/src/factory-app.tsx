"use client";

import { useEffect, useRef, useState } from "react";
import { FactoryAppController, type FactoryAppControllerOptions, type FactoryAppSnapshot } from "./factory-app-controller.js";
import { FactoryConsole } from "./factory-console.js";

const INITIAL_SNAPSHOT: FactoryAppSnapshot = { status: "idle" };
type ControllerFactory = (options: FactoryAppControllerOptions) => FactoryAppController;
const createController: ControllerFactory = (options) => new FactoryAppController(options);

/** Complete browser application lifecycle; hosts only render this component. */
export function FactoryApp() {
  return <FactoryAppLifecycle />;
}

/** Package-internal mounted-effect seam; absent from the package root export. */
export function FactoryAppLifecycle({ controllerFactory = createController }: { controllerFactory?: ControllerFactory }) {
  const [snapshot, setSnapshot] = useState<FactoryAppSnapshot>(INITIAL_SNAPSHOT);
  const owner = useRef<FactoryAppController | undefined>(undefined);

  useEffect(() => {
    const controller = controllerFactory({
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
  }, [controllerFactory]);

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
