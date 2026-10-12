-- name: GetHuddleInvitationByRequestKey :one
SELECT * FROM huddle_invitations
WHERE company_id=sqlc.arg(company_id)::uuid AND huddle_id=sqlc.arg(huddle_id)::uuid
  AND invitee_user_id=sqlc.arg(invitee_user_id)::uuid AND request_key=sqlc.arg(request_key)::uuid;

-- name: GetAcceptedHuddleInvitation :one
SELECT * FROM huddle_invitations
WHERE company_id=sqlc.arg(company_id)::uuid AND huddle_id=sqlc.arg(huddle_id)::uuid
  AND invitee_user_id=sqlc.arg(invitee_user_id)::uuid AND status='accepted'
ORDER BY updated_at DESC, id DESC LIMIT 1;

-- name: GetPendingHuddleInvitation :one
SELECT * FROM huddle_invitations
WHERE company_id=sqlc.arg(company_id)::uuid AND huddle_id=sqlc.arg(huddle_id)::uuid
  AND invitee_user_id=sqlc.arg(invitee_user_id)::uuid AND status='pending'
ORDER BY created_at DESC, id DESC LIMIT 1;

-- name: ExpirePendingHuddleInvitation :one
UPDATE huddle_invitations
SET status='expired', revision=revision+1, updated_at=now()
WHERE company_id=sqlc.arg(company_id)::uuid AND id=sqlc.arg(invitation_id)::uuid
  AND status='pending' AND expires_at <= now()
RETURNING *;

-- name: ListPendingHuddleInvitations :many
SELECT * FROM huddle_invitations
WHERE company_id=sqlc.arg(company_id)::uuid
  AND invitee_user_id=sqlc.arg(invitee_user_id)::uuid
  AND status='pending'
  AND expires_at > now()
ORDER BY created_at ASC, id ASC;

-- name: ListHuddleInvitationHistory :many
SELECT * FROM huddle_invitations
WHERE company_id=sqlc.arg(company_id)::uuid
  AND invitee_user_id=sqlc.arg(invitee_user_id)::uuid
  AND status <> 'pending'
ORDER BY updated_at DESC, id DESC
LIMIT 50;

-- name: ListHuddleInvitationContexts :many
-- Invitations identify their caller, but never grant access to the source chat.
-- The service separately uses the canonical Discussion access projection.
SELECT invitation.id, huddle.source_conversation_id,
       CASE WHEN inviter.deleted_at IS NOT NULL OR inviter.disabled_at IS NOT NULL
            THEN 'Former teammate' ELSE COALESCE(inviter.display_name, 'A teammate') END::text AS inviter_name
FROM huddle_invitations invitation
JOIN huddles huddle ON huddle.company_id=invitation.company_id AND huddle.id=invitation.huddle_id
LEFT JOIN users inviter ON inviter.id=invitation.inviter_user_id
WHERE invitation.company_id=sqlc.arg(company_id)::uuid
  AND invitation.invitee_user_id=sqlc.arg(invitee_user_id)::uuid
  AND invitation.id=ANY(sqlc.arg(invitation_ids)::uuid[]);

-- name: InsertHuddleInvitation :one
INSERT INTO huddle_invitations(company_id,huddle_id,inviter_user_id,invitee_user_id,request_key,expires_at)
VALUES(sqlc.arg(company_id)::uuid,sqlc.arg(huddle_id)::uuid,sqlc.arg(inviter_user_id)::uuid,sqlc.arg(invitee_user_id)::uuid,sqlc.arg(request_key)::uuid,sqlc.arg(expires_at)::timestamptz)
RETURNING *;

-- name: InsertInitialHuddleInvitation :one
INSERT INTO huddle_invitations(company_id,huddle_id,inviter_user_id,invitee_user_id,request_key,expires_at)
VALUES(sqlc.arg(company_id)::uuid,sqlc.arg(huddle_id)::uuid,sqlc.arg(inviter_user_id)::uuid,sqlc.arg(invitee_user_id)::uuid,sqlc.arg(request_key)::uuid,sqlc.arg(expires_at)::timestamptz)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetHuddleInvitationForUpdate :one
SELECT * FROM huddle_invitations
WHERE company_id=sqlc.arg(company_id)::uuid AND id=sqlc.arg(invitation_id)::uuid
FOR UPDATE;

-- name: UpdateHuddleInvitationStatus :one
UPDATE huddle_invitations
SET status=sqlc.arg(status)::text, revision=revision+1, updated_at=now()
WHERE company_id=sqlc.arg(company_id)::uuid AND id=sqlc.arg(invitation_id)::uuid
  AND status='pending' AND expires_at > now()
  AND revision=sqlc.arg(revision)::bigint
RETURNING *;

-- name: ExpireHuddleInvitations :many
UPDATE huddle_invitations
SET status='expired', revision=revision+1, updated_at=now()
WHERE status='pending' AND expires_at <= sqlc.arg(as_of)::timestamptz
RETURNING *;

-- name: RevokePendingHuddleInvitations :many
UPDATE huddle_invitations
SET status='revoked', revision=revision+1, updated_at=now()
WHERE company_id=sqlc.arg(company_id)::uuid AND huddle_id=sqlc.arg(huddle_id)::uuid
  AND status='pending'
RETURNING *;

-- name: ListUndeliveredMissedHuddleInvitations :many
SELECT invitation.id, invitation.company_id, invitation.invitee_user_id,
       huddle.source_conversation_id,
       source.kind AS source_kind,
       CASE WHEN inviter.deleted_at IS NOT NULL OR inviter.disabled_at IS NOT NULL
            THEN 'Former teammate' ELSE COALESCE(inviter.display_name, 'A teammate') END::text AS inviter_name
FROM huddle_invitations AS invitation
JOIN huddles AS huddle ON huddle.company_id = invitation.company_id
    AND huddle.id = invitation.huddle_id
JOIN conversations AS source ON source.company_id = huddle.company_id
    AND source.id = huddle.source_conversation_id
JOIN company_members AS member ON member.company_id = invitation.company_id
    AND member.user_id = invitation.invitee_user_id AND member.status = 'active'
LEFT JOIN users inviter ON inviter.id=invitation.inviter_user_id
WHERE invitation.status = 'expired'
  AND NOT EXISTS (
      SELECT 1 FROM inbox_items AS item
      WHERE item.company_id = invitation.company_id
        AND item.recipient_user_id = invitation.invitee_user_id
        AND item.dedupe_key = 'huddle-missed:' || invitation.id::text
  )
ORDER BY invitation.updated_at, invitation.id
LIMIT 50;
