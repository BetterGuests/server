-- +goose Up

-- The identity provider changed from Firebase to Clerk. Clerk also issues
-- string user IDs, so the column is repurposed in place rather than adding a
-- second identity column next to it.
ALTER TABLE app.accounts
    RENAME COLUMN firebase_uid TO clerk_user_id;

ALTER TABLE app.accounts
    RENAME CONSTRAINT accounts_firebase_uid_check TO accounts_clerk_user_id_check;

ALTER INDEX app.accounts_firebase_uid_idx
    RENAME TO accounts_clerk_user_id_idx;

-- +goose Down

ALTER INDEX app.accounts_clerk_user_id_idx
    RENAME TO accounts_firebase_uid_idx;

ALTER TABLE app.accounts
    RENAME CONSTRAINT accounts_clerk_user_id_check TO accounts_firebase_uid_check;

ALTER TABLE app.accounts
    RENAME COLUMN clerk_user_id TO firebase_uid;
