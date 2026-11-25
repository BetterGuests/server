package messages

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const summaryColumns = `c.id, c.listing_id, COALESCE(l.title, ''), c.guest_id, c.host_id,
	CASE WHEN c.host_id = $1 THEN 'host' ELSE 'guest' END,
	CASE WHEN c.host_id = $1 THEN COALESCE(gn.display_name, '` + missingGuestName + `') ELSE COALESCE(l.host_name, '` + missingHostName + `') END,
	CASE WHEN c.host_id = $1 THEN '' ELSE COALESCE(l.host_avatar_url, '') END,
	c.last_message_at, COALESCE(c.last_message_preview, ''),
	(SELECT count(*) FROM app.messages m
	 WHERE m.conversation_id = c.id AND m.sender_id <> $1
	   AND m.created_at > COALESCE(CASE WHEN c.host_id = $1 THEN c.host_read_at ELSE c.guest_read_at END, 'epoch'::timestamptz))`

const summaryFrom = `FROM app.conversations c
	LEFT JOIN app.listings l ON l.id = c.listing_id
	LEFT JOIN app.account_names gn ON gn.account_id = c.guest_id`

type PostgresMessageStore struct{ DB *pgxpool.Pool }

func NewPostgresMessageStore(pool *pgxpool.Pool) *PostgresMessageStore {
	return &PostgresMessageStore{DB: pool}
}

func (store *PostgresMessageStore) Open(
	ctx context.Context,
	actorID, newID uuid.UUID,
	target OpenTarget,
	now time.Time,
) (uuid.UUID, error) {
	var conversationID uuid.UUID
	err := db.WithUserTx(ctx, store.DB, actorID.String(), func(tx pgx.Tx) error {
		participants, listingID, err := resolveTarget(ctx, tx, actorID, target)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO app.accounts (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, actorID); err != nil {
			return fmt.Errorf("ensure account: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO app.conversations (id, listing_id, guest_id, host_id, created_at)
			 VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (listing_id, guest_id) DO NOTHING`,
			newID, listingID, participants.GuestID, participants.HostID, now); err != nil {
			return fmt.Errorf("insert conversation: %w", err)
		}
		if err := tx.QueryRow(ctx,
			`SELECT id FROM app.conversations WHERE listing_id = $1 AND guest_id = $2`,
			listingID, participants.GuestID).Scan(&conversationID); err != nil {
			return fmt.Errorf("select conversation: %w", err)
		}
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	return conversationID, nil
}

func resolveTarget(ctx context.Context, tx pgx.Tx, actorID uuid.UUID, target OpenTarget) (Participants, uuid.UUID, error) {
	var (
		participants Participants
		listingID    uuid.UUID
		err          error
	)
	if target.BookingID != uuid.Nil {
		err = tx.QueryRow(ctx,
			`SELECT b.listing_id, b.guest_id, l.host_id
			 FROM app.bookings b JOIN app.listings l ON l.id = b.listing_id
			 WHERE b.id = $1 AND (b.guest_id = $2 OR l.host_id = $2)`,
			target.BookingID, actorID).Scan(&listingID, &participants.GuestID, &participants.HostID)
	} else {
		participants.GuestID = actorID
		listingID = target.ListingID
		err = tx.QueryRow(ctx,
			`SELECT host_id FROM app.listings WHERE id = $1 AND status = 'published'`,
			target.ListingID).Scan(&participants.HostID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return Participants{}, uuid.Nil, ErrConversationNotFound
	}
	if err != nil {
		return Participants{}, uuid.Nil, fmt.Errorf("resolve conversation target: %w", err)
	}
	if participants.GuestID == participants.HostID {
		return Participants{}, uuid.Nil, fmt.Errorf("%w: you cannot message yourself", ErrInvalidConversation)
	}
	return participants, listingID, nil
}

func (store *PostgresMessageStore) Get(ctx context.Context, actorID, conversationID uuid.UUID) (Summary, error) {
	var summary Summary
	err := db.WithUserTx(ctx, store.DB, actorID.String(), func(tx pgx.Tx) error {
		var err error
		summary, err = scanSummary(tx.QueryRow(ctx,
			`SELECT `+summaryColumns+` `+summaryFrom+` WHERE c.id = $2 AND (c.guest_id = $1 OR c.host_id = $1)`,
			actorID, conversationID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Summary{}, ErrConversationNotFound
	}
	if err != nil {
		return Summary{}, fmt.Errorf("select conversation: %w", err)
	}
	return summary, nil
}

func (store *PostgresMessageStore) List(ctx context.Context, actorID uuid.UUID) ([]Summary, error) {
	var summaries []Summary
	err := db.WithUserTx(ctx, store.DB, actorID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+summaryColumns+` `+summaryFrom+`
			 WHERE c.guest_id = $1 OR c.host_id = $1
			 ORDER BY COALESCE(c.last_message_at, c.created_at) DESC
			 LIMIT $2`, actorID, maxListedThreads)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			summary, err := scanSummary(rows)
			if err != nil {
				return err
			}
			summaries = append(summaries, summary)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	if summaries == nil {
		summaries = []Summary{}
	}
	return summaries, nil
}

func (store *PostgresMessageStore) Messages(
	ctx context.Context,
	actorID, conversationID uuid.UUID,
	before *time.Time,
	limit int,
) ([]Message, error) {
	messages := []Message{}
	err := db.WithUserTx(ctx, store.DB, actorID.String(), func(tx pgx.Tx) error {
		if err := requireParticipant(ctx, tx, actorID, conversationID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx,
			`SELECT m.id, m.conversation_id, m.sender_id,
			        CASE WHEN m.sender_id = c.host_id THEN 'host' ELSE 'guest' END, m.body, m.created_at
			 FROM app.messages m JOIN app.conversations c ON c.id = m.conversation_id
			 WHERE m.conversation_id = $1 AND ($2::timestamptz IS NULL OR m.created_at < $2)
			 ORDER BY m.created_at DESC, m.id DESC
			 LIMIT $3`, conversationID, before, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var message Message
			if err := rows.Scan(&message.ID, &message.ConversationID, &message.SenderID, &message.SenderRole, &message.Body, &message.CreatedAt); err != nil {
				return err
			}
			messages = append(messages, message)
		}
		return rows.Err()
	})
	if errors.Is(err, ErrConversationNotFound) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	return messages, nil
}

func (store *PostgresMessageStore) Insert(
	ctx context.Context,
	actorID, conversationID, messageID uuid.UUID,
	body string,
	now time.Time,
) (Message, Participants, error) {
	var (
		message      Message
		participants Participants
	)
	err := db.WithUserTx(ctx, store.DB, actorID.String(), func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx,
			`SELECT guest_id, host_id FROM app.conversations
			 WHERE id = $1 AND (guest_id = $2 OR host_id = $2)`,
			conversationID, actorID).Scan(&participants.GuestID, &participants.HostID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConversationNotFound
		}
		if err != nil {
			return fmt.Errorf("select conversation: %w", err)
		}

		role := RoleGuest
		readColumn := "guest_read_at"
		if participants.HostID == actorID {
			role = RoleHost
			readColumn = "host_read_at"
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO app.messages (id, conversation_id, sender_id, body, created_at) VALUES ($1, $2, $3, $4, $5)`,
			messageID, conversationID, actorID, body, now); err != nil {
			return fmt.Errorf("insert message: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE app.conversations
			 SET last_message_at = $2, last_message_preview = $3, `+readColumn+` = $2
			 WHERE id = $1`,
			conversationID, now, preview(body)); err != nil {
			return fmt.Errorf("update conversation: %w", err)
		}
		message = Message{
			ID: messageID, ConversationID: conversationID, SenderID: actorID,
			SenderRole: role, Body: body, CreatedAt: now,
		}
		return nil
	})
	if err != nil {
		return Message{}, Participants{}, err
	}
	return message, participants, nil
}

func (store *PostgresMessageStore) MarkRead(ctx context.Context, actorID, conversationID uuid.UUID, now time.Time) error {
	return db.WithUserTx(ctx, store.DB, actorID.String(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE app.conversations
			 SET guest_read_at = CASE WHEN guest_id = $2 THEN $3 ELSE guest_read_at END,
			     host_read_at = CASE WHEN host_id = $2 THEN $3 ELSE host_read_at END
			 WHERE id = $1 AND (guest_id = $2 OR host_id = $2)`,
			conversationID, actorID, now)
		if err != nil {
			return fmt.Errorf("mark conversation read: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrConversationNotFound
		}
		return nil
	})
}

func requireParticipant(ctx context.Context, tx pgx.Tx, actorID, conversationID uuid.UUID) error {
	var participant bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM app.conversations WHERE id = $1 AND (guest_id = $2 OR host_id = $2))`,
		conversationID, actorID).Scan(&participant); err != nil {
		return fmt.Errorf("check participant: %w", err)
	}
	if !participant {
		return ErrConversationNotFound
	}
	return nil
}

func scanSummary(row pgx.Row) (Summary, error) {
	var summary Summary
	err := row.Scan(
		&summary.ID, &summary.ListingID, &summary.ListingTitle, &summary.GuestID, &summary.HostID,
		&summary.Role, &summary.CounterpartyName, &summary.CounterpartyAvatarURL,
		&summary.LastMessageAt, &summary.LastMessagePreview, &summary.UnreadCount,
	)
	return summary, err
}
