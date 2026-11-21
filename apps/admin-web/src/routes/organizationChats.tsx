/**
 * The "Telegram chats" tab of the organization drawer, and the "Operator chats" panel of the organizations page
 * (spec 08_architecture/61, section 7a.4). The superadmin lists the chats of the sales bot, adds one with a
 * one-time code, changes what it hears, switches it off and deletes it. Every write asks for a reason of its own.
 *
 * Adding a chat never asks for a chat number: the screen shows the code and the exact command, the operator adds
 * the sales bot to the group and posts the command there, and the bot binds the chat itself.
 *
 * The presentational parts take plain props so they render without a router or a query client; `SalesChatsPanel`
 * owns the queries and the writes. A null `orgId` is the platform's own list (chats that hear every organization).
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import {
  CHAT_KINDS,
  buildAddChatRequest,
  buildEditChatRequest,
  chatErrorMessage,
  chatState,
  countdownLabel,
  createSalesChatCode,
  deleteSalesChat,
  editChatForm,
  emptyAddChatForm,
  kindHint,
  kindLabel,
  kindsLabel,
  listSalesChats,
  patchSalesChat,
  secondsLeft,
  toggleKind,
  whenLabel,
  type AddChatForm,
  type ChatKind,
  type EditChatForm,
  type SalesChat,
  type SalesChatConnectCode,
} from "@/lib/api/salesChats";
import {
  Dialog,
  Pill,
  buttonStyle,
  dangerButtonStyle,
  disabledButtonStyle,
  errorStyle,
  inputStyle,
  labelStyle,
  mutedStyle,
  noticeStyle,
  primaryButtonStyle,
  sectionStyle,
  sectionTitleStyle,
  tableStyle,
  tdStyle,
  thStyle,
} from "./onboardingUi";

type Modal =
  | { kind: "none" }
  | { kind: "add"; form: AddChatForm }
  | { kind: "code"; code: SalesChatConnectCode }
  | { kind: "edit"; chat: SalesChat; form: EditChatForm }
  | { kind: "toggle"; chat: SalesChat; reason: string }
  | { kind: "delete"; chat: SalesChat; reason: string };

export const BOT_HANDLE = "@ArenaSoldOutSalesBot";

// ---------------------------------------------------------------------------
// Presentational parts
// ---------------------------------------------------------------------------

export function ChatsView({
  chats,
  scopeLabel,
  notice,
  onAdd,
  onEdit,
  onToggle,
  onDelete,
}: {
  chats: readonly SalesChat[];
  /** "this organization" or "the platform". */
  scopeLabel: string;
  notice?: string | null;
  onAdd: () => void;
  onEdit: (c: SalesChat) => void;
  onToggle: (c: SalesChat) => void;
  onDelete: (c: SalesChat) => void;
}): JSX.Element {
  return (
    <section style={sectionStyle} data-testid="sales-chats">
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", gap: 8, flexWrap: "wrap" }}>
        <h3 style={sectionTitleStyle}>Telegram chats of {scopeLabel}</h3>
        <button type="button" style={primaryButtonStyle} onClick={onAdd} data-testid="sales-chat-add">
          Add chat
        </button>
      </div>
      {notice != null ? (
        <div style={noticeStyle} role="status" data-testid="sales-chat-notice">
          {notice}
        </div>
      ) : null}
      {chats.length === 0 ? (
        <p style={{ ...mutedStyle, marginTop: 8 }} data-testid="sales-chats-empty">
          No chats. Add one, then post the code in the Telegram group.
        </p>
      ) : (
        <div style={{ overflowX: "auto", marginTop: 8 }}>
          <table style={tableStyle} data-testid="sales-chats-table">
            <thead>
              <tr>
                {["Name", "Gets", "State", "Telegram chat", "Connected", "Last error", ""].map((h, i) => (
                  <th key={`${h}-${i}`} style={thStyle}>
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {chats.map((c) => {
                const st = chatState(c);
                return (
                  <tr key={c.id} data-testid={`sales-chat-${c.id}`}>
                    <td style={tdStyle}>{c.name}</td>
                    <td style={tdStyle} data-testid={`sales-chat-kinds-${c.id}`}>
                      {kindsLabel(c.kinds)}
                    </td>
                    <td style={tdStyle}>
                      <Pill tone={st.tone} testId={`sales-chat-state-${c.id}`}>
                        {st.label}
                      </Pill>
                    </td>
                    <td style={tdStyle}>{c.chat_title != null && c.chat_title !== "" ? c.chat_title : c.chat_id === "" ? "Not connected" : c.chat_id}</td>
                    <td style={tdStyle}>{whenLabel(c.connected_at)}</td>
                    <td style={{ ...tdStyle, color: c.last_error != null ? "#b91c1c" : undefined }} data-testid={`sales-chat-error-${c.id}`}>
                      {c.last_error ?? ""}
                    </td>
                    <td style={{ ...tdStyle, whiteSpace: "nowrap" }}>
                      <button type="button" style={buttonStyle} onClick={() => onEdit(c)} data-testid={`sales-chat-edit-${c.id}`}>
                        Edit
                      </button>{" "}
                      <button type="button" style={buttonStyle} onClick={() => onToggle(c)} data-testid={`sales-chat-toggle-${c.id}`}>
                        {c.allowed ? "Turn off" : "Turn on"}
                      </button>{" "}
                      <button type="button" style={dangerButtonStyle} onClick={() => onDelete(c)} data-testid={`sales-chat-delete-${c.id}`}>
                        Delete
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

export function ReasonField({ value, onChange }: { value: string; onChange: (v: string) => void }): JSX.Element {
  return (
    <label style={labelStyle}>
      Reason (kept in the audit log)
      <input style={inputStyle} value={value} onChange={(e) => onChange(e.target.value)} data-testid="sales-chat-reason" />
    </label>
  );
}

export function KindsField({ kinds, onChange }: { kinds: readonly ChatKind[]; onChange: (k: ChatKind[]) => void }): JSX.Element {
  return (
    <fieldset style={{ border: "1px solid #e2e8f0", borderRadius: 4, padding: "8px 12px", margin: 0 }}>
      <legend style={{ fontSize: 12, fontWeight: 600, color: "#334155", padding: "0 4px" }}>What the chat gets</legend>
      {CHAT_KINDS.map((k) => (
        <label key={k} style={{ display: "block", fontSize: 13, margin: "4px 0" }}>
          <input
            type="checkbox"
            checked={kinds.includes(k)}
            onChange={(e) => onChange(toggleKind(kinds, k, e.target.checked))}
            data-testid={`sales-chat-kind-${k}`}
          />{" "}
          <strong>{kindLabel(k)}</strong>
          <span style={{ ...mutedStyle, fontSize: 12, display: "block", marginLeft: 22 }}>{kindHint(k)}</span>
        </label>
      ))}
      <p style={{ ...mutedStyle, fontSize: 12, marginTop: 6 }}>A chat with Shop sales and Delivery gets one message with the full card.</p>
    </fieldset>
  );
}

export function AddChatBody({ form, onChange }: { form: AddChatForm; onChange: (f: AddChatForm) => void }): JSX.Element {
  return (
    <>
      <label style={labelStyle}>
        Name of the chat
        <input
          style={inputStyle}
          maxLength={200}
          placeholder="Delivery group, Warsaw"
          value={form.name}
          onChange={(e) => onChange({ ...form, name: e.target.value })}
          data-testid="sales-chat-name"
        />
      </label>
      <KindsField kinds={form.kinds} onChange={(kinds) => onChange({ ...form, kinds })} />
      <ReasonField value={form.reason} onChange={(reason) => onChange({ ...form, reason })} />
    </>
  );
}

export function EditChatBody({ form, onChange }: { form: EditChatForm; onChange: (f: EditChatForm) => void }): JSX.Element {
  return (
    <>
      <label style={labelStyle}>
        Name of the chat
        <input style={inputStyle} maxLength={200} value={form.name} onChange={(e) => onChange({ ...form, name: e.target.value })} data-testid="sales-chat-name" />
      </label>
      <KindsField kinds={form.kinds} onChange={(kinds) => onChange({ ...form, kinds })} />
      <ReasonField value={form.reason} onChange={(reason) => onChange({ ...form, reason })} />
    </>
  );
}

/** A ticking "29:41"; `now` is fixed in tests, and the interval only runs in a browser. */
export function Countdown({ expiresAt, now }: { expiresAt: string; now?: Date }): JSX.Element {
  const [tick, setTick] = useState(() => now ?? new Date());
  useEffect(() => {
    if (now !== undefined) return undefined;
    const id = window.setInterval(() => setTick(new Date()), 1000);
    return () => window.clearInterval(id);
  }, [now]);
  const left = secondsLeft(expiresAt, tick);
  return (
    <span data-testid="sales-chat-countdown" style={{ fontVariantNumeric: "tabular-nums", color: left === 0 ? "#b91c1c" : undefined }}>
      {countdownLabel(left)}
    </span>
  );
}

/** The code, shown once: the exact command to post in the group and a button that copies it. */
export function CodeBody({
  code,
  now,
  copied,
  onCopy,
}: {
  code: SalesChatConnectCode;
  now?: Date;
  copied: boolean;
  onCopy: () => void;
}): JSX.Element {
  return (
    <>
      <p style={{ margin: 0, fontSize: 13 }} data-testid="sales-chat-instruction">
        Add {BOT_HANDLE} to the Telegram group, then post this command there. The bot answers with the organization that the chat gets messages for.
      </p>
      <div style={{ display: "flex", gap: 8, alignItems: "center", flexWrap: "wrap" }}>
        <code
          style={{ fontSize: 18, background: "#f1f5f9", padding: "8px 12px", borderRadius: 4, userSelect: "all" }}
          data-testid="sales-chat-command"
        >
          {code.command}
        </code>
        <button type="button" style={buttonStyle} onClick={onCopy} data-testid="sales-chat-copy">
          {copied ? "Copied" : "Copy"}
        </button>
      </div>
      <p style={{ ...mutedStyle, fontSize: 12 }}>
        Works one time. Expires in <Countdown expiresAt={code.expires_at} {...(now !== undefined ? { now } : {})} />. The window shows the code one time. If you close the window,
        you must make a new code.
      </p>
      <p style={{ ...mutedStyle, fontSize: 12 }}>
        Chat: {code.name}. Gets: {kindsLabel(code.kinds)}.
      </p>
    </>
  );
}

export function Problems({ errors, serverError }: { errors: readonly string[]; serverError: string | null }): JSX.Element | null {
  if (errors.length === 0 && serverError === null) return null;
  return (
    <div style={errorStyle} role="alert" data-testid="sales-chat-problems">
      {errors.map((e) => (
        <div key={e}>{e}</div>
      ))}
      {serverError !== null ? <div>{serverError}</div> : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// The panel
// ---------------------------------------------------------------------------

export function SalesChatsPanel({ orgId, scopeLabel }: { orgId: string | null; scopeLabel: string }): JSX.Element {
  const qc = useQueryClient();
  const key = ["admin", "sales-chats", orgId ?? "operator"] as const;
  const [modal, setModal] = useState<Modal>({ kind: "none" });
  const [errors, setErrors] = useState<readonly string[]>([]);
  const [serverError, setServerError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const chats = useQuery({ queryKey: key, queryFn: () => listSalesChats(orgId), retry: false, refetchOnWindowFocus: false });

  const close = (): void => {
    setModal({ kind: "none" });
    setErrors([]);
    setServerError(null);
    setCopied(false);
    // The bot may have bound the chat while the code was open.
    void qc.invalidateQueries({ queryKey: key });
  };
  const done = (text: string): void => {
    setModal({ kind: "none" });
    setErrors([]);
    setServerError(null);
    setNotice(text);
    void qc.invalidateQueries({ queryKey: key });
  };

  const addChat = useMutation({
    mutationFn: (v: { request: Parameters<typeof createSalesChatCode>[1]; reason: string }) => createSalesChatCode(orgId, v.request, v.reason),
    onSuccess: (code) => {
      setErrors([]);
      setServerError(null);
      setNotice(null);
      setModal({ kind: "code", code });
    },
    onError: (e) => setServerError(chatErrorMessage(e)),
  });
  const editChat = useMutation({
    mutationFn: (v: { id: string; request: Parameters<typeof patchSalesChat>[1]; reason: string }) => patchSalesChat(v.id, v.request, v.reason),
    onSuccess: (c) => done(`The chat "${c.name}" is saved.`),
    onError: (e) => setServerError(chatErrorMessage(e)),
  });
  const toggleChat = useMutation({
    mutationFn: (v: { chat: SalesChat; reason: string }) => patchSalesChat(v.chat.id, { allowed: !v.chat.allowed }, v.reason),
    onSuccess: (c) => done(c.allowed ? `The chat "${c.name}" is on.` : `The chat "${c.name}" is off. Nothing is sent there.`),
    onError: (e) => setServerError(chatErrorMessage(e)),
  });
  const removeChat = useMutation({
    mutationFn: (v: { chat: SalesChat; reason: string }) => deleteSalesChat(v.chat.id, v.reason),
    onSuccess: (_r, v) => done(`The chat "${v.chat.name}" is deleted. Its delivery history stays.`),
    onError: (e) => setServerError(chatErrorMessage(e)),
  });
  const busy = addChat.isPending || editChat.isPending || toggleChat.isPending || removeChat.isPending;

  const open = (m: Modal): void => {
    setNotice(null);
    setErrors([]);
    setServerError(null);
    setCopied(false);
    setModal(m);
  };

  const submit = (): void => {
    setServerError(null);
    if (modal.kind === "add") {
      const built = buildAddChatRequest(modal.form);
      if (!built.ok) return setErrors(built.errors);
      setErrors([]);
      addChat.mutate({ request: built.request, reason: built.reason });
    } else if (modal.kind === "edit") {
      const built = buildEditChatRequest(modal.chat, modal.form);
      if (!built.ok) return setErrors(built.errors);
      setErrors([]);
      editChat.mutate({ id: modal.chat.id, request: built.request, reason: built.reason });
    } else if (modal.kind === "toggle") {
      if (modal.reason.trim() === "") return setErrors(["Reason: write the cause. The audit log keeps it."]);
      setErrors([]);
      toggleChat.mutate({ chat: modal.chat, reason: modal.reason.trim() });
    } else if (modal.kind === "delete") {
      if (modal.reason.trim() === "") return setErrors(["Reason: write the cause. The audit log keeps it."]);
      setErrors([]);
      removeChat.mutate({ chat: modal.chat, reason: modal.reason.trim() });
    }
  };

  const copy = (code: SalesChatConnectCode): void => {
    try {
      void navigator.clipboard.writeText(code.command).then(
        () => setCopied(true),
        () => setCopied(false),
      );
    } catch {
      setCopied(false);
    }
  };

  const footer = (saveTestId: string, label: string, danger = false): JSX.Element => (
    <>
      <button type="button" style={buttonStyle} onClick={close}>
        Cancel
      </button>
      <button
        type="submit"
        style={{ ...(danger ? dangerButtonStyle : primaryButtonStyle), ...(busy ? disabledButtonStyle : {}) }}
        disabled={busy}
        data-testid={saveTestId}
      >
        {label}
      </button>
    </>
  );

  return (
    <div data-testid={orgId === null ? "operator-chats-panel" : "org-chats-tab"}>
      {chats.isPending ? <p style={mutedStyle}>Loading...</p> : null}
      {chats.error ? (
        <div style={errorStyle} role="alert" data-testid="sales-chats-error">
          {chatErrorMessage(chats.error)}
        </div>
      ) : null}
      {chats.data !== undefined ? (
        <ChatsView
          chats={chats.data.items}
          scopeLabel={scopeLabel}
          notice={notice}
          onAdd={() => open({ kind: "add", form: emptyAddChatForm() })}
          onEdit={(chat) => open({ kind: "edit", chat, form: editChatForm(chat) })}
          onToggle={(chat) => open({ kind: "toggle", chat, reason: "" })}
          onDelete={(chat) => open({ kind: "delete", chat, reason: "" })}
        />
      ) : null}

      {modal.kind === "add" ? (
        <Dialog title="Add a Telegram chat" testId="sales-chat-add-dialog" onClose={close} onSubmit={submit} footer={footer("sales-chat-add-save", "Make a code")}>
          <AddChatBody form={modal.form} onChange={(form) => setModal({ kind: "add", form })} />
          <Problems errors={errors} serverError={serverError} />
        </Dialog>
      ) : null}

      {modal.kind === "code" ? (
        <Dialog
          title="Connect the chat"
          testId="sales-chat-code-dialog"
          onClose={close}
          footer={
            <button type="button" style={primaryButtonStyle} onClick={close} data-testid="sales-chat-code-done">
              Done
            </button>
          }
        >
          <CodeBody code={modal.code} copied={copied} onCopy={() => copy(modal.code)} />
        </Dialog>
      ) : null}

      {modal.kind === "edit" ? (
        <Dialog title="Edit the chat" testId="sales-chat-edit-dialog" onClose={close} onSubmit={submit} footer={footer("sales-chat-edit-save", "Save")}>
          <EditChatBody form={modal.form} onChange={(form) => setModal({ ...modal, form })} />
          <Problems errors={errors} serverError={serverError} />
        </Dialog>
      ) : null}

      {modal.kind === "toggle" ? (
        <Dialog
          title={modal.chat.allowed ? "Turn the chat off" : "Turn the chat on"}
          testId="sales-chat-toggle-dialog"
          onClose={close}
          onSubmit={submit}
          footer={footer("sales-chat-toggle-save", modal.chat.allowed ? "Turn off" : "Turn on")}
        >
          <p style={{ margin: 0, fontSize: 13 }}>
            {modal.chat.allowed
              ? `Nothing is sent to "${modal.chat.name}" while it is off. Its settings stay.`
              : `"${modal.chat.name}" starts hearing ${kindsLabel(modal.chat.kinds)} again.`}
          </p>
          <ReasonField value={modal.reason} onChange={(reason) => setModal({ ...modal, reason })} />
          <Problems errors={errors} serverError={serverError} />
        </Dialog>
      ) : null}

      {modal.kind === "delete" ? (
        <Dialog title="Delete the chat" testId="sales-chat-delete-dialog" onClose={close} onSubmit={submit} footer={footer("sales-chat-delete-save", "Delete", true)}>
          <p style={{ margin: 0, fontSize: 13 }}>
            Delete "{modal.chat.name}"? The platform sends no more messages there. The delivery history stays. To use the group again, add it with a new code.
          </p>
          <ReasonField value={modal.reason} onChange={(reason) => setModal({ ...modal, reason })} />
          <Problems errors={errors} serverError={serverError} />
        </Dialog>
      ) : null}
    </div>
  );
}

/** The drawer tab of one organization. */
export function OrganizationChatsTab({ orgId }: { orgId: string }): JSX.Element {
  return <SalesChatsPanel orgId={orgId} scopeLabel="this organization" />;
}

/** The platform's own chats (they hear every organization), opened from the organizations page. */
export function OperatorChatsPanel(): JSX.Element {
  return <SalesChatsPanel orgId={null} scopeLabel="the platform (every organization)" />;
}
