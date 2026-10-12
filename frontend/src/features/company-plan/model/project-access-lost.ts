// A read of a Project's content answered 404: the viewer may no longer read
// the Project (`404 project_not_found`, ADR 0017), or it is gone. That is not
// a load error to show in red until the Project itself is read again
// (`useProjectAccessLoss`): the access-loss check takes the viewer off a
// Project they lost and says so once.
export function isProjectAccessLost(error: unknown): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    "status" in error &&
    error.status === 404
  );
}
