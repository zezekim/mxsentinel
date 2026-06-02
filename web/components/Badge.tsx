import type { StatusColor, OverallStatus, Severity } from "@/lib/api";

type BadgeProps =
  | { kind: "status"; value: StatusColor | OverallStatus }
  | { kind: "severity"; value: Severity };

export default function Badge(props: BadgeProps) {
  if (props.kind === "status") {
    const cls = `badge badge-${props.value}`;
    return <span className={cls}>{props.value}</span>;
  }
  // severity
  const cls = `badge sev-${props.value}`;
  return <span className={cls}>{props.value}</span>;
}
