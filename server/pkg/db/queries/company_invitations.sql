-- name: ListCompanyInvitations :many
SELECT i.*, t.delivery_status FROM company_invitations i
JOIN company_invitation_tokens t ON t.invitation_id=i.id AND t.generation=i.generation
WHERE i.company_id=$1 ORDER BY i.created_at DESC,i.id DESC LIMIT 100;

-- name: GetCompanyInvitationForUpdate :one
SELECT * FROM company_invitations WHERE id=$1 AND company_id=$2 FOR UPDATE;

-- name: FindCompanyInvitationToken :one
SELECT sqlc.embed(i), t.generation AS token_generation, t.delivery_status, c.name AS company_name,
 u.display_name AS inviter_name
FROM company_invitation_tokens t JOIN company_invitations i ON i.id=t.invitation_id
JOIN companies c ON c.id=i.company_id JOIN users u ON u.id=i.invited_by_user_id
WHERE t.token_hash=$1 AND c.deleting_at IS NULL;

-- name: ExpireCompanyInvitations :many
UPDATE company_invitations SET status='expired',version=version+1,updated_at=now()
WHERE company_id=$1 AND email=$2 AND status='pending' AND expires_at<=now()
RETURNING *;

-- name: GetCompanyInvitationDeliveryStatus :one
SELECT delivery_status FROM company_invitation_tokens WHERE invitation_id=$1 AND generation=$2;

-- name: FindPendingCompanyInvitation :one
SELECT * FROM company_invitations WHERE company_id=$1 AND email=$2 AND status='pending';

-- name: InvitationEmailIsMember :one
SELECT EXISTS(SELECT 1 FROM company_members m JOIN users u ON u.id=m.user_id
 WHERE m.company_id=$1 AND lower(u.email)=$2 AND m.status='active')::boolean;

-- name: CreateCompanyInvitation :one
INSERT INTO company_invitations(company_id,email,invited_by_user_id,role,expires_at)
VALUES($1,$2,$3,$4,now()+interval '7 days') RETURNING *;

-- name: InsertCompanyInvitationToken :exec
INSERT INTO company_invitation_tokens(invitation_id,generation,token_hash,encrypted_token,recipient_email,company_name,inviter_name,expires_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8);

-- name: ResendCompanyInvitation :one
UPDATE company_invitations SET status='pending',generation=generation+1,expires_at=now()+interval '7 days',
 invited_by_user_id=$3,version=version+1,updated_at=now(),renewal_requested_at=NULL
WHERE id=$1 AND company_id=$2 RETURNING *;

-- name: ResolveCompanyInvitation :one
UPDATE company_invitations SET status=$3,accepted_user_id=$4,updated_at=now(),version=version+1
WHERE id=$1 AND company_id=$2 RETURNING *;

-- name: CancelCompanyInvitationDelivery :exec
UPDATE company_invitation_tokens SET encrypted_token=NULL,
 delivery_status=CASE WHEN delivery_status='sent' THEN 'sent' ELSE 'cancelled' END,updated_at=now(),lease_until=NULL
WHERE invitation_id=$1;

-- name: RequestCompanyInvitationRenewal :one
UPDATE company_invitations SET renewal_requested_at=now(),version=version+1
WHERE id=$1 AND (renewal_requested_at IS NULL OR renewal_requested_at<now()-interval '1 day') RETURNING *;

-- name: ExpireCompanyInvitationDelivery :exec
UPDATE company_invitation_tokens t SET encrypted_token=NULL,delivery_status='failed',lease_until=NULL,updated_at=now()
WHERE delivery_status IN ('queued','sending') AND (t.created_at < now()-interval '23 hours' OR NOT EXISTS(
 SELECT 1 FROM company_invitations i JOIN companies c ON c.id=i.company_id
 WHERE i.id=t.invitation_id AND i.generation=t.generation AND i.status='pending' AND i.expires_at>now() AND c.deleting_at IS NULL));

-- name: ClaimCompanyInvitationDelivery :one
WITH candidate AS (
 SELECT t.invitation_id,t.generation FROM company_invitation_tokens t
 JOIN company_invitations i ON i.id=t.invitation_id AND i.generation=t.generation
 JOIN companies c ON c.id=i.company_id
 WHERE t.delivery_status IN ('queued','sending') AND t.next_attempt_at<=now()
 AND (t.lease_until IS NULL OR t.lease_until<now()) AND t.created_at>now()-interval '23 hours'
 AND i.status='pending' AND i.expires_at>now() AND c.deleting_at IS NULL
 ORDER BY t.next_attempt_at LIMIT 1 FOR UPDATE OF t SKIP LOCKED
)
UPDATE company_invitation_tokens t SET delivery_status='sending',attempts=t.attempts+1,
 lease_until=now()+interval '2 minutes',updated_at=now()
FROM candidate WHERE t.invitation_id=candidate.invitation_id AND t.generation=candidate.generation RETURNING t.*;

-- name: FinishCompanyInvitationDelivery :exec
UPDATE company_invitation_tokens SET delivery_status=$4,provider_id=$5,
 encrypted_token=CASE WHEN $4::text IN ('sent','failed') THEN NULL ELSE encrypted_token END,
 next_attempt_at=now()+interval '1 minute',lease_until=NULL,updated_at=now()
WHERE invitation_id=$1 AND generation=$2 AND attempts=$3 AND delivery_status='sending';

-- name: CountRecentCompanyInvitations :one
SELECT count(*) FROM company_invitations WHERE company_id=$1 AND created_at>now()-interval '1 hour';
