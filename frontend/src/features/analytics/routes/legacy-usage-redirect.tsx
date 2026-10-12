import { Navigate, useLocation } from "react-router";

export function LegacyUsageRedirect() {
  const { search, hash } = useLocation();
  const params = new URLSearchParams(search);
  const agent = params.get("agent") || params.get("agent_id");
  if (agent && !params.has("tab")) params.set("tab", "agents");

  if (!params.has("range") && !params.has("start") && !params.has("end")) {
    const period = legacyUsagePeriod(params.get("period"));
    if (period) {
      params.set("range", "custom");
      params.set("start", period.start);
      params.set("end", period.end);
    }
  }

  return (
    <Navigate
      replace
      to={{ pathname: "/analytics", search: params.toString(), hash }}
    />
  );
}

function legacyUsagePeriod(period: string | null) {
  const today = new Date();
  const year = today.getFullYear();
  const month = today.getMonth();
  let start: Date;
  let end: Date;

  // The old period filter uses calendar boundaries, with Monday as week start.
  switch (period) {
    case "week":
      start = new Date(
        year,
        month,
        today.getDate() - ((today.getDay() + 6) % 7),
      );
      end = new Date(start);
      end.setDate(start.getDate() + 6);
      break;
    case "month":
      start = new Date(year, month, 1);
      end = new Date(year, month + 1, 0);
      break;
    case "year":
      start = new Date(year, 0, 1);
      end = new Date(year, 11, 31);
      break;
    default:
      return null;
  }

  const date = (value: Date) =>
    `${value.getFullYear()}-${String(value.getMonth() + 1).padStart(2, "0")}-${String(value.getDate()).padStart(2, "0")}`;
  return { start: date(start), end: date(end) };
}
