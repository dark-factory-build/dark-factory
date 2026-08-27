"use client";

import { useEffect, useRef } from "react";
import fitAddon from "@xterm/addon-fit";
import xterm from "@xterm/xterm";
import type { TerminalSurface } from "./terminal-controller.js";

// The packages publish a browser module but expose a CommonJS main to Node;
// default interop keeps SSR/Node consumers from evaluating named CJS imports.
const { FitAddon } = fitAddon as unknown as { FitAddon: typeof import("@xterm/addon-fit").FitAddon };
const { Terminal } = xterm as unknown as { Terminal: typeof import("@xterm/xterm").Terminal };

export type XtermTerminalProps = Readonly<{
  onSurface: (surface: TerminalSurface | undefined) => void;
  onData?: (value: string) => void;
  onBinary?: (value: string) => void;
  onResize?: (rows: number, cols: number) => void;
}>;

/** Thin DOM/xterm adapter; terminal authority stays in TerminalController. */
export function XtermTerminal({ onSurface, onData, onBinary, onResize }: XtermTerminalProps) {
  const element = useRef<HTMLDivElement>(null);
  const callbacks = useRef({ onSurface, onData, onBinary, onResize });
  callbacks.current = { onSurface, onData, onBinary, onResize };

  useEffect(() => {
    const container = element.current;
    if (container === null) return;
    const terminal = new Terminal();
    const fit = new FitAddon();
    terminal.loadAddon(fit);
    terminal.open(container);
    let initialResize = true;
    const data = terminal.onData((value) => callbacks.current.onData?.(value));
    const binary = terminal.onBinary((value) => callbacks.current.onBinary?.(value));
    const resized = terminal.onResize(({ rows, cols }) => { if (!initialResize) callbacks.current.onResize?.(rows, cols); });
    fit.fit();
    initialResize = false;
    callbacks.current.onResize?.(terminal.rows, terminal.cols);
    const fitAndResize = () => fit.fit();
    const pendingWrites = new Set<{ resolve: () => void; reject: (error: Error) => void }>();
    const surface: TerminalSurface = {
      write: (payload) => new Promise<void>((resolve, reject) => {
        const pending = { resolve, reject };
        pendingWrites.add(pending);
        try {
          terminal.write(payload, () => {
            if (pendingWrites.delete(pending)) resolve();
          });
        } catch {
          pendingWrites.delete(pending);
          reject(new Error("terminal display failed"));
        }
      }),
      abort: () => {
        const error = new Error("terminal display disposed");
        for (const pending of pendingWrites) pending.reject(error);
        pendingWrites.clear();
      },
    };
    callbacks.current.onSurface(surface);
    terminal.focus();
    const onWindowResize = () => fitAndResize();
    window.addEventListener("resize", onWindowResize);
    return () => {
      window.removeEventListener("resize", onWindowResize);
      surface.abort();
      data.dispose();
      binary.dispose();
      resized.dispose();
      callbacks.current.onSurface(undefined);
      fit.dispose();
      terminal.dispose();
    };
  }, []);

  return <div ref={element} className="dfFactoryConsole__terminal" role="application" aria-label="Agent terminal" />;
}
