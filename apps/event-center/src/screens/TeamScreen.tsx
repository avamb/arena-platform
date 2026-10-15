/**
 * 4-5: the team of the organization. The web twin of the bot's Team screen
 * (eventbot/team.go): who is on the team, where each invitation stands, invite
 * a person by e-mail (the letter opens the web event center or the Telegram
 * bot), send a Telegram invitation again, revoke one, remove a member.
 *
 * What a person sees and may do comes from the permissions the server reports
 * for THIS organization (`can`), never from the role: reading the list is
 * `membership.read`, inviting is `membership.grant`, removing and revoking
 * are `membership.revoke`.
 */
import { useId, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ConfirmPressDialog } from "@/components/ConfirmPressDialog";
import { ErrorNotice } from "@/components/ErrorNotice";
import { Badge, Empty, ErrorBox, Loading } from "@/components/ui";
import { fetchTeam, inviteMember, removeMember, resendInvitation, revokeInvitation, type InviteChannel, type InviteRole } from "@/lib/api/endpoints";
import { ApiError } from "@/lib/api/http";
import type { BotTeamMember } from "@/lib/api/types";
import { useMe } from "@/lib/auth";
import { describeApiError } from "@/lib/errors";
import { formatDateTime } from "@/lib/format";
import { useI18n, type Locale } from "@/lib/i18n";
import {
  INVITE_LOCALES,
  canRemoveRow,
  channelKey,
  isRevokable,
  looksLikeEmail,
  memberState,
  memberStateKey,
  retryMinutes,
  type MemberState,
} from "@/lib/team";
import { roleKey } from "@/screens/OrgsScreen";
import { useOrg } from "@/screens/OrgLayout";

interface Notice {
  tone: "ok" | "bad";
  title: string;
  next?: string;
}

const STATE_TONE: Record<MemberState, "ok" | "wait" | "bad" | "neutral"> = {
  accepted: "ok",
  telegram: "ok",
  added: "neutral",
  waiting: "wait",
  expired: "bad",
};

function InviteForm({ orgId, onDone }: { orgId: string; onDone: (n: Notice) => void }) {
  const { t, locale } = useI18n();
  const ids = useId();
  const queryClient = useQueryClient();
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<InviteRole>("manager");
  const [channel, setChannel] = useState<InviteChannel>("web");
  const [letterLocale, setLetterLocale] = useState<Locale>(locale);
  const [problem, setProblem] = useState<"email" | null>(null);

  const invite = useMutation({
    mutationFn: () => inviteMember(orgId, { email: email.trim(), role, channel, locale: letterLocale }),
    onSuccess: async (res) => {
      const effective = res.invitation.role;
      const keptRole = effective !== role;
      onDone({
        tone: "ok",
        title: t(channel === "web" ? "team.invited.web" : "team.invited.telegram", { email: res.invitation.email }),
        next: keptRole
          ? t("team.invited.keptRole", { role: t(effective === "owner" ? "role.org_admin" : "role.organizer") })
          : t(channel === "web" ? "team.invited.webNext" : "team.invited.telegramNext"),
      });
      setEmail("");
      await queryClient.invalidateQueries({ queryKey: ["team", orgId] });
    },
  });

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (invite.isPending) return;
    if (!looksLikeEmail(email)) {
      setProblem("email");
      return;
    }
    setProblem(null);
    invite.mutate();
  };

  return (
    <form className="team-invite" onSubmit={onSubmit} noValidate aria-labelledby={`${ids}-title`} data-testid="team-invite-form">
      <h2 id={`${ids}-title`}>{t("team.invite.title")}</h2>
      <label htmlFor={`${ids}-email`}>{t("team.invite.email")}</label>
      <input
        id={`${ids}-email`}
        type="email"
        autoComplete="off"
        value={email}
        onChange={(e) => setEmail(e.target.value)}
        aria-invalid={problem === "email"}
        aria-describedby={problem === "email" ? `${ids}-email-note` : undefined}
        data-testid="team-invite-email"
      />
      {problem === "email" && (
        <p id={`${ids}-email-note`} className="notice notice-bad" role="alert" data-testid="team-invite-email-note">
          {t("team.invite.emailNote")}
        </p>
      )}
      <label htmlFor={`${ids}-role`}>{t("team.invite.role")}</label>
      <select id={`${ids}-role`} value={role} onChange={(e) => setRole(e.target.value as InviteRole)} data-testid="team-invite-role">
        <option value="manager">{t("role.organizer")}</option>
        <option value="owner">{t("role.org_admin")}</option>
      </select>
      <p className="muted">{t(role === "owner" ? "team.invite.roleOwnerHint" : "team.invite.roleManagerHint")}</p>
      <label htmlFor={`${ids}-channel`}>{t("team.invite.channel")}</label>
      <select
        id={`${ids}-channel`}
        value={channel}
        onChange={(e) => setChannel(e.target.value as InviteChannel)}
        data-testid="team-invite-channel"
      >
        <option value="web">{t("team.channel.web")}</option>
        <option value="telegram">{t("team.channel.telegram")}</option>
      </select>
      <p className="muted">{t(channel === "web" ? "team.invite.channelWebHint" : "team.invite.channelTelegramHint")}</p>
      <label htmlFor={`${ids}-locale`}>{t("team.invite.locale")}</label>
      <select
        id={`${ids}-locale`}
        value={letterLocale}
        onChange={(e) => setLetterLocale(e.target.value as Locale)}
        data-testid="team-invite-locale"
      >
        {INVITE_LOCALES.map((l) => (
          <option key={l} value={l}>
            {t(`team.locale.${l}`)}
          </option>
        ))}
      </select>
      {invite.error !== null && <ErrorNotice error={invite.error} testId="team-invite-error" />}
      <p>
        <button type="submit" className="btn btn-primary" disabled={invite.isPending} data-testid="team-invite-submit">
          {invite.isPending ? t("confirmWord.working") : t("team.invite.submit")}
        </button>
      </p>
    </form>
  );
}

function MemberRow({
  member,
  selfId,
  canGrant,
  canRevoke,
  busy,
  onResend,
  onRevoke,
  onRemove,
}: {
  member: BotTeamMember;
  selfId: string | undefined;
  canGrant: boolean;
  canRevoke: boolean;
  busy: boolean;
  onResend: (m: BotTeamMember) => void;
  onRevoke: (m: BotTeamMember) => void;
  onRemove: (m: BotTeamMember) => void;
}) {
  const { t, locale } = useI18n();
  const state = memberState(member);
  const channel = channelKey(member.invitation_channel);
  const self = selfId !== undefined && member.user_id === selfId;
  const revokable = isRevokable(member);
  return (
    <li className="card team-row" data-testid="team-member" data-email={member.email} data-state={state}>
      <span className="card-title">
        {member.email}
        {self && <span className="muted"> {t("team.you")}</span>}
      </span>
      <span className="card-meta">
        <span data-testid="team-role">{t(roleKey(member.membership_role))}</span>
        <span data-testid="team-state">
          <Badge tone={STATE_TONE[state]}>{t(memberStateKey(state))}</Badge>
        </span>
        {channel !== null && <span className="muted" data-testid="team-channel">{t(channel)}</span>}
      </span>
      {member.invitation_expires_at !== undefined && state === "waiting" && (
        <span className="muted">{t("team.until", { date: formatDateTime(member.invitation_expires_at, null, locale) })}</span>
      )}
      <span className="team-actions">
        {canGrant && revokable && (
          <button type="button" className="btn" disabled={busy} onClick={() => onResend(member)} data-testid="team-resend">
            {t("team.resend")}
          </button>
        )}
        {canRevoke && revokable && (
          <button type="button" className="btn" disabled={busy} onClick={() => onRevoke(member)} data-testid="team-revoke">
            {t("team.revoke")}
          </button>
        )}
        {canRevoke && canRemoveRow(member, selfId) && (
          <button type="button" className="btn" disabled={busy} onClick={() => onRemove(member)} data-testid="team-remove">
            {t("team.remove")}
          </button>
        )}
      </span>
    </li>
  );
}

export function TeamScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  const queryClient = useQueryClient();
  const me = useMe();
  const selfId = me.data?.user.id;
  const canRead = access.can("membership.read");
  const canGrant = access.can("membership.grant");
  const canRevoke = access.can("membership.revoke");
  const [notice, setNotice] = useState<Notice | null>(null);
  const [removing, setRemoving] = useState<BotTeamMember | null>(null);
  const [revoking, setRevoking] = useState<BotTeamMember | null>(null);
  const [resending, setResending] = useState<string | null>(null);

  const team = useQuery({ queryKey: ["team", orgId], queryFn: () => fetchTeam(orgId), enabled: canRead });
  const reload = () => queryClient.invalidateQueries({ queryKey: ["team", orgId] });

  const onResend = async (m: BotTeamMember) => {
    if (m.invitation_id === undefined || resending !== null) return;
    setResending(m.user_id);
    setNotice(null);
    try {
      await resendInvitation(orgId, m.invitation_id);
      setNotice({ tone: "ok", title: t("team.resent", { email: m.email }), next: t("team.resentNext") });
    } catch (err) {
      const minutes = err instanceof ApiError && err.status === 429 ? retryMinutes(err) : undefined;
      const text = describeApiError(err, t, { minutes: minutes ?? 10 });
      setNotice({ tone: "bad", title: text.title, next: text.next });
    } finally {
      setResending(null);
      await reload();
    }
  };

  const onRevokeConfirm = async () => {
    if (revoking === null || revoking.invitation_id === undefined) return;
    const res = await revokeInvitation(orgId, revoking.invitation_id);
    setRevoking(null);
    setNotice({
      tone: "ok",
      title: t("team.revoked", { email: revoking.email }),
      next: t(res.membership_removed ? "team.revokedRemoved" : "team.revokedKept"),
    });
    await reload();
  };

  const onRemoveConfirm = async () => {
    if (removing === null) return;
    await removeMember(orgId, removing.user_id, removing.membership_role);
    setNotice({ tone: "ok", title: t("team.removed", { email: removing.email }), next: t("team.removedNext") });
    setRemoving(null);
    await reload();
  };

  return (
    <section aria-labelledby="team-title">
      <h1 id="team-title">{t("team.title")}</h1>
      {!canRead ? (
        <p className="notice" role="status" data-testid="team-no-access">
          {t("team.noAccess")}
        </p>
      ) : (
        <>
          <p className="muted">{t("team.intro")}</p>
          {notice !== null && (
            <div className={`notice notice-${notice.tone}`} role={notice.tone === "bad" ? "alert" : "status"} data-testid="team-notice">
              <p>
                <strong>{notice.title}</strong>
              </p>
              {notice.next !== undefined && <p>{notice.next}</p>}
            </div>
          )}
          {team.isLoading && <Loading />}
          {team.error !== null && <ErrorBox error={team.error} onRetry={() => void team.refetch()} />}
          {team.data !== undefined && team.data.members.length === 0 && <Empty>{t("team.empty")}</Empty>}
          {team.data !== undefined && (
            <ul className="cards" data-testid="team-list">
              {team.data.members.map((m) => (
                <MemberRow
                  key={m.user_id}
                  member={m}
                  selfId={selfId}
                  canGrant={canGrant}
                  canRevoke={canRevoke}
                  busy={resending !== null}
                  onResend={(x) => void onResend(x)}
                  onRevoke={setRevoking}
                  onRemove={setRemoving}
                />
              ))}
            </ul>
          )}
          {canGrant ? <InviteForm orgId={orgId} onDone={setNotice} /> : <p className="muted" data-testid="team-read-only">{t("team.readOnly")}</p>}
        </>
      )}
      {removing !== null && (
        <ConfirmPressDialog
          testId="team-remove-dialog"
          title={t("team.remove.title")}
          subject={removing.email}
          explain={t("team.remove.explain", { role: t(roleKey(removing.membership_role)) })}
          confirmLabel={t("team.remove.confirm")}
          onConfirm={onRemoveConfirm}
          onCancel={() => setRemoving(null)}
        />
      )}
      {revoking !== null && (
        <ConfirmPressDialog
          testId="team-revoke-dialog"
          title={t("team.revoke.title")}
          subject={revoking.email}
          explain={t("team.revoke.explain")}
          confirmLabel={t("team.revoke.confirm")}
          onConfirm={onRevokeConfirm}
          onCancel={() => setRevoking(null)}
        />
      )}
    </section>
  );
}
