const beijingTimeZone = "Asia/Shanghai";

const dateTimeFormatter = new Intl.DateTimeFormat("zh-CN", {
  timeZone: beijingTimeZone,
  hourCycle: "h23",
  year: "numeric",
  month: "numeric",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit"
});

const dateFormatter = new Intl.DateTimeFormat("zh-CN", {
  timeZone: beijingTimeZone,
  year: "numeric",
  month: "numeric",
  day: "numeric"
});

const clockFormatter = new Intl.DateTimeFormat("zh-CN", {
  timeZone: beijingTimeZone,
  hourCycle: "h23",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit"
});

export function formatBeijingDateTime(value?: string) {
  if (!value) return "-";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : dateTimeFormatter.format(date);
}

export function formatBeijingClock(value = new Date()) {
  return clockFormatter.format(value);
}

export function formatBeijingTimeAgo(value?: string, now = Date.now()) {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  const diff = Math.max(0, now - date.getTime());
  if (diff < 60_000) return "刚刚";
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)} 分钟前`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)} 小时前`;
  return dateFormatter.format(date);
}
