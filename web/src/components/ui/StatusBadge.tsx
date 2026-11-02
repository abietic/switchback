import type { StatusTone } from "../../api/contracts";

export function StatusBadge({ label, tone }: { label: string; tone: StatusTone }) {
  return (
    <span aria-label={label} className={`status-badge status-${tone}`}>
      {label}
    </span>
  );
}
