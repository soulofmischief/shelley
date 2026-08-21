package db

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"shelley.exe.dev/chatgptauth"
	"shelley.exe.dev/db/generated"
)

func (db *DB) LoadChatGPTCredentials(ctx context.Context) (chatgptauth.Credentials, error) {
	var credentials chatgptauth.Credentials
	err := db.Queries(ctx, func(q *generated.Queries) error {
		row, err := q.GetChatGPTOAuthCredentials(ctx)
		if err != nil {
			return err
		}
		credentials = chatgptauth.Credentials{
			AccessToken:  row.AccessToken,
			RefreshToken: row.RefreshToken,
			IDToken:      row.IDToken,
			AccountID:    row.AccountID,
			ExpiresAt:    time.Unix(row.ExpiresAt, 0),
		}
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		return chatgptauth.Credentials{}, chatgptauth.ErrNotAuthenticated
	}
	return credentials, err
}

func (db *DB) SaveChatGPTCredentials(ctx context.Context, credentials chatgptauth.Credentials) error {
	return db.QueriesTx(ctx, func(q *generated.Queries) error {
		return q.SaveChatGPTOAuthCredentials(ctx, generated.SaveChatGPTOAuthCredentialsParams{
			AccessToken:  credentials.AccessToken,
			RefreshToken: credentials.RefreshToken,
			IDToken:      credentials.IDToken,
			AccountID:    credentials.AccountID,
			ExpiresAt:    credentials.ExpiresAt.Unix(),
		})
	})
}

func (db *DB) DeleteChatGPTCredentials(ctx context.Context) error {
	return db.QueriesTx(ctx, func(q *generated.Queries) error {
		return q.DeleteChatGPTOAuthCredentials(ctx)
	})
}
