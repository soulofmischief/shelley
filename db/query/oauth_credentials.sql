-- name: GetOAuthCredentials :one
SELECT * FROM oauth_credentials WHERE provider = ?;

-- name: UpsertOAuthCredentials :one
INSERT INTO oauth_credentials (provider, access_token, refresh_token, account_id, expires_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(provider) DO UPDATE SET
    access_token = excluded.access_token,
    refresh_token = excluded.refresh_token,
    account_id = excluded.account_id,
    expires_at = excluded.expires_at,
    updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- name: DeleteOAuthCredentials :exec
DELETE FROM oauth_credentials WHERE provider = ?;

-- name: GetAllOAuthCredentials :many
SELECT * FROM oauth_credentials;
