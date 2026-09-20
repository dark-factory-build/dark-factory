import type { FormEvent } from "react";

export type AnswerControlsProps = Readonly<{
  surface: "factory" | "remote";
  options: readonly string[];
  canReply: boolean;
  reply: string;
  replyMaxBytes: number;
  busy: boolean;
  disabled?: boolean;
  inputId?: string;
  onReplyChange?: (reply: string) => void;
  onReply?: () => void;
  submitLabel?: string;
  submittingLabel?: string;
}>;

/** Shared question choices and reply form; authority stays with each app. */
export function AnswerControls({
  surface,
  options,
  canReply,
  reply,
  replyMaxBytes,
  busy,
  disabled = false,
  inputId = surface === "factory" ? "dfHumanRequestReply" : "dfRemoteReply",
  onReplyChange,
  onReply,
  submitLabel = "ANSWER",
  submittingLabel = "ANSWERING…",
}: AnswerControlsProps) {
  const submit = (event: FormEvent) => { event.preventDefault(); onReply?.(); };
  const factory = surface === "factory";
  return (
    <>
      {options.length === 0 ? null : <div className="dfFactoryConsole__answerOptions" role="group" aria-label="Suggested answers">
        {options.map((option, index) => <button type="button" key={option} disabled={busy || disabled || !canReply || onReplyChange === undefined || onReply === undefined} onClick={() => { onReplyChange?.(option); onReply?.(); }}>{option}{index === 0 ? " · RECOMMENDED" : ""}</button>)}
      </div>}
      {!canReply ? null : <form className={factory ? "dfFactoryConsole__reply" : "dfRemote__reply"} aria-label="Answer this question" onSubmit={submit}>
        <label htmlFor={inputId}>YOUR ANSWER</label>
        <textarea
          id={inputId}
          className={factory ? undefined : "dfRemote__replyText"}
          value={reply}
          maxLength={replyMaxBytes}
          disabled={busy || disabled || onReplyChange === undefined}
          onChange={(event) => onReplyChange?.(event.currentTarget.value)}
        />
        <button
          type={factory ? "submit" : "button"}
          className={factory ? undefined : "dfRemote__replyAction"}
          disabled={busy || disabled || onReply === undefined || reply.trim().length === 0}
          onClick={factory ? undefined : onReply}
        >
          {busy ? submittingLabel : submitLabel}
        </button>
      </form>}
    </>
  );
}
