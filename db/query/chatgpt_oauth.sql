-- name: GetChatGPTOAuthCredentials :one
SELECT access_token, refresh_token, id_token, account_id, expires_at
FROM chatgpt_oauth_credentials
WHERE provider = 'openai';

-- name: SaveChatGPTOAuthCredentials :exec
INSERT INTO chatgpt_oauth_credentials (
    provider, access_token, refresh_token, id_token, account_id, expires_at, updated_at
) VALUES (
    'openai', ?, ?, ?, ?, ?, CURRENT_TIMESTAMP
)
ON CONFLICT(provider) DO UPDATE SET
    access_token = excluded.access_token,
    refresh_token = excluded.refresh_token,
    id_token = excluded.id_token,
    account_id = excluded.account_id,
    expires_at = excluded.expires_at,
    updated_at = CURRENT_TIMESTAMP;

-- name: DeleteChatGPTOAuthCredentials :exec
DELETE FROM chatgpt_oauth_credentials WHERE provider = 'openai';
