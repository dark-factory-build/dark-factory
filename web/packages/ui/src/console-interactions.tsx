import type { FormEvent } from "react";

export type AnswerControlsProps = Readonly<{
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

/** Question choices and reply form; authority stays with the app. */
export function AnswerControls({
  options,
  canReply,
  reply,
  replyMaxBytes,
  busy,
  disabled = false,
  inputId = "dfHumanRequestReply",
  onReplyChange,
  onReply,
  submitLabel = "Answer",
  submittingLabel = "Answering…",
}: AnswerControlsProps) {
  const submit = (event: FormEvent) => { event.preventDefault(); onReply?.(); };
  return (
    <>
      {options.length === 0 ? null : <div className="dfFactoryConsole__answerOptions" role="group" aria-label="Suggested answers">
        {options.map((option, index) => <button type="button" key={option} disabled={busy || disabled || !canReply || onReplyChange === undefined || onReply === undefined} onClick={() => { onReplyChange?.(option); onReply?.(); }}>{option}{index === 0 ? " · Recommended" : ""}</button>)}
      </div>}
      {!canReply ? null : <form className="dfFactoryConsole__reply" aria-label="Answer this question" onSubmit={submit}>
        <label htmlFor={inputId}>Your answer</label>
        <textarea
          id={inputId}
          value={reply}
          maxLength={replyMaxBytes}
          disabled={busy || disabled || onReplyChange === undefined}
          onChange={(event) => onReplyChange?.(event.currentTarget.value)}
        />
        <button type="submit" disabled={busy || disabled || onReply === undefined || reply.trim().length === 0}>
          {busy ? submittingLabel : submitLabel}
        </button>
      </form>}
    </>
  );
}
