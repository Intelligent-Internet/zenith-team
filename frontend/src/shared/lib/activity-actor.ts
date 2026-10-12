import type { ActivityDto } from "@/shared/api/types";

type ActivityActorFields = {
  actor: ActivityDto["actor"];
  onBehalfOf: ActivityDto["onBehalfOf"];
};

// Who did an Activity row's action, as the row reads it. An Agent acting in
// a Run reads "Eli for Mia": the Agent, then the person the Run works for.
// Rows from before Activity kept that person read as they always have.
export function activityActorLabel({ actor, onBehalfOf }: ActivityActorFields) {
  const name = activityActorName(actor);
  return onBehalfOf ? `${name} for ${activityPersonName(onBehalfOf)}` : name;
}

export function activityActorName(actor: ActivityDto["actor"]) {
  return (
    actor.displayName || (actor.kind === "system" ? "System" : "Unknown actor")
  );
}

export function activityPersonName(
  person: NonNullable<ActivityDto["onBehalfOf"]>,
) {
  return person.displayName || "Unknown member";
}
