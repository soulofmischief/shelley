-- OAuth credentials table
-- Stores OAuth tokens for providers that use OAuth instead of API keys (e.g., Codex/ChatGPT)
-- One row per provider - credentials are shared across all models using that provider

CREATE TABLE oauth_credentials (
    provider TEXT PRIMARY KEY,  -- e.g., 'codex'
    access_token TEXT NOT NULL,
    refresh_token TEXT NOT NULL,
    account_id TEXT,  -- ChatGPT account ID for the ChatGPT-Account-ID header
    expires_at INTEGER NOT NULL,  -- Unix timestamp when access_token expires
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Add 'codex' to the allowed provider_type values in models table
-- SQLite doesn't support ALTER TABLE to modify CHECK constraints,
-- so we need to recreate the table

-- Step 1: Create new table with updated constraint
CREATE TABLE models_new (
    model_id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    provider_type TEXT NOT NULL CHECK (provider_type IN ('anthropic', 'openai', 'openai-responses', 'gemini', 'codex')),
    endpoint TEXT NOT NULL,
    api_key TEXT NOT NULL DEFAULT '',  -- Empty for OAuth-based providers like codex
    model_name TEXT NOT NULL,
    max_tokens INTEGER NOT NULL DEFAULT 200000,
    tags TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Step 2: Copy existing data
INSERT INTO models_new SELECT * FROM models;

-- Step 3: Drop old table and rename new one
DROP TABLE models;
ALTER TABLE models_new RENAME TO models;
