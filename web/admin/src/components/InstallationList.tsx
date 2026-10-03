import { ArrowRightLeft, Smartphone } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import {
  INSTALLATION_SWITCH_HINT,
  installationAccountText,
  installationIsStandby,
  installationModelText,
  installationShortId,
  installationStateLabel,
  installationStateTone,
  installationSwitchPending,
  installationVersionsText
} from "@/installations";
import { formatBeijingDateTime, formatBeijingTimeAgo } from "@/time";
import type { ModuleInstallation, ModuleSwitchRequest } from "@/types";

export type InstallationNotice = { text: string; tone: "info" | "error" };

type InstallationListProps = {
  installations?: ModuleInstallation[];
  switchRequest?: ModuleSwitchRequest;
  /** One line per phone, for narrow lists and tables. */
  compact?: boolean;
  /** Installation id whose switch request is in flight. */
  switchingId?: number | null;
  disabled?: boolean;
  /** Omit to render a read-only list without switch buttons. */
  onSwitch?: (installation: ModuleInstallation) => void;
  notice?: InstallationNotice | null;
  hint?: boolean;
  className?: string;
};

const standbyBadgeClass = "border-sky-300 text-sky-700 dark:border-sky-500/40 dark:text-sky-200";

/** Phones (installations) that used one device binding; hidden for old servers without the field. */
export function InstallationList({
  installations,
  switchRequest,
  compact = false,
  switchingId = null,
  disabled = false,
  onSwitch,
  notice,
  hint = false,
  className
}: InstallationListProps) {
  if (!installations || installations.length === 0) return null;
  const inFlight = switchingId !== null;
  return (
    <div className={cn("grid gap-2", className)} data-testid="installation-list">
      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 text-xs">
        <span className="flex items-center gap-1 font-medium text-foreground">
          <Smartphone className="h-3.5 w-3.5" />
          手机
          <span className="font-normal text-muted-foreground">· {installations.length} 台</span>
        </span>
        {switchRequest ? (
          <span className="text-amber-700 dark:text-amber-200">
            切换请求有效至 {formatBeijingDateTime(switchRequest.expires_at)}
          </span>
        ) : null}
      </div>
      <ul className={compact ? "grid gap-1" : "grid gap-1.5"}>
        {installations.map((item) => {
          const pending = installationSwitchPending(item, switchRequest);
          const canSwitch = !item.active && !!onSwitch;
          const switchButton = canSwitch ? (
            <Button
              type="button"
              size="xs"
              variant="outline"
              className={compact ? "shrink-0" : "w-full shrink-0 sm:w-auto"}
              disabled={disabled || inFlight}
              onClick={(event) => {
                event.stopPropagation();
                onSwitch?.(item);
              }}
            >
              <ArrowRightLeft className="h-3.5 w-3.5" />
              {switchingId === item.id ? "请求中" : "切到这台手机"}
            </Button>
          ) : null;
          const badges = (
            <>
              <Badge
                variant={installationStateTone(item)}
                className={installationIsStandby(item) ? standbyBadgeClass : undefined}
              >
                {installationStateLabel(item)}
              </Badge>
              {pending ? <Badge variant="warning">切换中</Badge> : null}
            </>
          );
          const seen = (
            <span title={formatBeijingDateTime(item.last_seen_at)}>
              {compact ? formatBeijingTimeAgo(item.last_seen_at) : `最近在线 ${formatBeijingTimeAgo(item.last_seen_at)}`}
            </span>
          );
          if (compact) {
            return (
              <li key={item.id} className="flex flex-wrap items-center gap-1.5 text-xs" data-testid="installation-row">
                {badges}
                <span className="min-w-0 truncate font-medium text-foreground">{installationModelText(item)}</span>
                <span className="font-mono text-muted-foreground">{installationShortId(item)}</span>
                <span className="text-muted-foreground">{seen}</span>
                {switchButton}
              </li>
            );
          }
          const versions = installationVersionsText(item);
          const account = installationAccountText(item);
          return (
            <li
              key={item.id}
              className="grid gap-2 rounded-md border bg-background p-2 text-xs sm:flex sm:items-start sm:justify-between sm:gap-3"
              data-testid="installation-row"
            >
              <div className="grid min-w-0 gap-1">
                <div className="flex flex-wrap items-center gap-1.5">
                  {badges}
                  <span className="min-w-0 break-all font-medium text-foreground">{installationModelText(item)}</span>
                  <span className="font-mono text-muted-foreground">{installationShortId(item)}</span>
                </div>
                {versions ? <div className="text-muted-foreground">{versions}</div> : null}
                <div className="break-all text-muted-foreground">
                  {account ? <span>{account} · </span> : null}
                  {seen}
                  {!item.active && item.last_active_at ? (
                    <span title={formatBeijingDateTime(item.last_active_at)}> · 上次在用 {formatBeijingTimeAgo(item.last_active_at)}</span>
                  ) : null}
                </div>
              </div>
              {switchButton}
            </li>
          );
        })}
      </ul>
      {notice ? (
        <p role="status" className={notice.tone === "error" ? "break-words text-xs text-destructive" : "break-words text-xs text-muted-foreground"}>
          {notice.text}
        </p>
      ) : null}
      {hint ? <InstallationSwitchHint /> : null}
    </div>
  );
}

export function InstallationSwitchHint({ className }: { className?: string }) {
  return <p className={cn("text-xs leading-5 text-muted-foreground", className)}>{INSTALLATION_SWITCH_HINT}</p>;
}
