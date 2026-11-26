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
	CASE WHEN c.host_id = $1 THEN COALESCE(gn.avatar_url, '') ELSE COALESCE(l.host_avatar_url, hn.avatar_url, '') END,
	c.last_message_at, COALESCE(c.last_message_preview, ''),
	(SELECT count(*) FROM app.messages m
	 WHERE m.conversation_id = c.id AND m.sender_id <> $1
	   AND m.created_at > COALESCE(CASE WHEN c.host_id = $1 THEN c.host_read_at ELSE c.guest_read_at END, 'epoch'::timestamptz)),
	NOT EXISTS (SELECT 1 FROM app.bookings b WHERE ` + stayStillOpen + `)`

const stayStillOpen = `b.listing_id = c.listing_id AND b.guest_id = c.guest_id
	AND (b.status IN ('requested', 'pending') OR (b.status = 'confirmed' AND b.ends_on >= current_date))`

const hiddenAtFor = `CASE WHEN c.host_id = $1 THEN c.host_hidden_at ELSE c.guest_hidden_at END`

const summaryFrom = `FROM app.conversations c
	LEFT JOIN app.listings l ON l.id = c.listing_id
	LEFT JOIN app.account_names gn ON gn.account_id = c.guest_id
	LEFT JOIN app.account_names hn ON hn.account_id = c.host_id`

const messageColumns = `m.id, m.conversation_id, COALESCE(m.sender_id, '00000000-0000-0000-0000-000000000000'::uuid),
	CASE WHEN m.sender_id IS NULL THEN 'system' WHEN m.sender_id = c.host_id THEN 'host' ELSE 'guest' END,
	m.body, m.created_at, m.booking_id,
	r.id,
	CASE WHEN r.id IS NULL THEN NULL WHEN r.sender_id IS NULL THEN 'system' WHEN r.sender_id = c.host_id THEN 'host' ELSE 'guest' END,
	left(r.body, ` + replyPreviewRunesSQL + `),
	ARRAY(SELECT x.emoji FROM app.message_reactions x WHERE x.message_id = m.id ORDER BY x.created_at, x.id),
	ARRAY(SELECT CASE WHEN x.account_id = c.host_id THEN 'host' ELSE 'guest' END
	      FROM app.message_reactions x WHERE x.message_id = m.id ORDER BY x.created_at, x.id)`

const messageFrom = `FROM app.messages m
	JOIN app.conversations c ON c.id = m.conversation_id
	LEFT JOIN app.messages r ON r.id = m.reply_to_id`

const replyPreviewRunesSQL = "200"

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
		if err != nil {
			return err
		}
		counterparty := summary.HostID
		if summary.Role == RoleHost {
			counterparty = summary.GuestID
		}
		if err := tx.QueryRow(ctx, `SELECT app.reply_minutes($1)`, counterparty).Scan(&summary.CounterpartyReplyMins); err != nil {
			return fmt.Errorf("reply minutes: %w", err)
		}
		return nil
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
			 WHERE (c.guest_id = $1 OR c.host_id = $1)
			   AND (`+hiddenAtFor+` IS NULL OR COALESCE(c.last_message_at, c.created_at) > `+hiddenAtFor+`)
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
			`SELECT `+messageColumns+` `+messageFrom+`
			 WHERE m.conversation_id = $2 AND ($3::timestamptz IS NULL OR m.created_at < $3)
			   AND m.created_at > COALESCE(`+hiddenAtFor+`, '-infinity'::timestamptz)
			 ORDER BY m.created_at DESC, m.id DESC
			 LIMIT $4`, actorID, conversationID, before, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			message, err := scanMessage(rows)
			if err != nil {
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
	actorID, conversationID uuid.UUID,
	draft Draft,
	now time.Time,
) (Message, Participants, bool, error) {
	var (
		message      Message
		participants Participants
		created      bool
	)
	err := db.WithUserTx(ctx, store.DB, actorID.String(), func(tx pgx.Tx) error {
		var err error
		participants, err = lockParticipants(ctx, tx, actorID, conversationID)
		if err != nil {
			return err
		}
		readColumn := "guest_read_at"
		if participants.HostID == actorID {
			readColumn = "host_read_at"
		}
		var replyTo *uuid.UUID
		if draft.ReplyToID != uuid.Nil {
			if err := requireMessageIn(ctx, tx, conversationID, draft.ReplyToID); err != nil {
				return err
			}
			replyTo = &draft.ReplyToID
		}
		tag, err := tx.Exec(ctx,
			`INSERT INTO app.messages (id, conversation_id, sender_id, body, created_at, reply_to_id)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 ON CONFLICT (id) DO NOTHING`,
			draft.ID, conversationID, actorID, draft.Body, now, replyTo)
		if err != nil {
			return fmt.Errorf("insert message: %w", err)
		}
		created = tag.RowsAffected() == 1
		if created {
			if _, err := tx.Exec(ctx,
				`UPDATE app.conversations
				 SET last_message_at = $2, last_message_preview = $3, `+readColumn+` = $2
				 WHERE id = $1`,
				conversationID, now, preview(draft.Body)); err != nil {
				return fmt.Errorf("update conversation: %w", err)
			}
		}
		message, err = selectMessage(ctx, tx, conversationID, draft.ID)
		if errors.Is(err, ErrMessageNotFound) || (err == nil && message.SenderID != actorID) {
			return fmt.Errorf("%w: that message id is already taken", ErrInvalidMessage)
		}
		return err
	})
	if err != nil {
		return Message{}, Participants{}, false, err
	}
	return message, participants, created, nil
}

func (store *PostgresMessageStore) Delete(ctx context.Context, actorID, conversationID, messageID uuid.UUID) (Participants, error) {
	var participants Participants
	err := db.WithUserTx(ctx, store.DB, actorID.String(), func(tx pgx.Tx) error {
		var err error
		participants, err = lockParticipants(ctx, tx, actorID, conversationID)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`DELETE FROM app.messages WHERE id = $1 AND conversation_id = $2 AND sender_id = $3`,
			messageID, conversationID, actorID)
		if err != nil {
			return fmt.Errorf("delete message: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrMessageNotFound
		}
		if _, err := tx.Exec(ctx,
			`UPDATE app.conversations c
			 SET last_message_at = latest.created_at, last_message_preview = left(latest.body, $2)
			 FROM (SELECT max(created_at) AS created_at,
			              (array_agg(body ORDER BY created_at DESC, id DESC))[1] AS body
			       FROM app.messages WHERE conversation_id = $1) latest
			 WHERE c.id = $1`,
			conversationID, previewRunes); err != nil {
			return fmt.Errorf("refresh conversation preview: %w", err)
		}
		return nil
	})
	if err != nil {
		return Participants{}, err
	}
	return participants, nil
}

func (store *PostgresMessageStore) ToggleReaction(
	ctx context.Context,
	actorID, conversationID, messageID, reactionID uuid.UUID,
	emoji string,
	now time.Time,
) ([]Reaction, Participants, error) {
	var (
		reactions    []Reaction
		participants Participants
	)
	err := db.WithUserTx(ctx, store.DB, actorID.String(), func(tx pgx.Tx) error {
		var err error
		participants, err = lockParticipants(ctx, tx, actorID, conversationID)
		if err != nil {
			return err
		}
		if err := requireMessageIn(ctx, tx, conversationID, messageID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO app.accounts (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, actorID); err != nil {
			return fmt.Errorf("ensure account: %w", err)
		}
		removed, err := tx.Exec(ctx,
			`DELETE FROM app.message_reactions WHERE message_id = $1 AND account_id = $2 AND emoji = $3`,
			messageID, actorID, emoji)
		if err != nil {
			return fmt.Errorf("remove reaction: %w", err)
		}
		if removed.RowsAffected() == 0 {
			if _, err := tx.Exec(ctx,
				`INSERT INTO app.message_reactions (id, message_id, account_id, emoji, created_at)
				 VALUES ($1, $2, $3, $4, $5)
				 ON CONFLICT (message_id, account_id, emoji) DO NOTHING`,
				reactionID, messageID, actorID, emoji, now); err != nil {
				return fmt.Errorf("add reaction: %w", err)
			}
		}
		message, err := selectMessage(ctx, tx, conversationID, messageID)
		if err != nil {
			return err
		}
		reactions = message.Reactions
		return nil
	})
	if err != nil {
		return nil, Participants{}, err
	}
	return reactions, participants, nil
}

func (store *PostgresMessageStore) Participants(ctx context.Context, actorID, conversationID uuid.UUID) (Participants, error) {
	var participants Participants
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
		return nil
	})
	return participants, err
}

func lockParticipants(ctx context.Context, tx pgx.Tx, actorID, conversationID uuid.UUID) (Participants, error) {
	var participants Participants
	err := tx.QueryRow(ctx,
		`SELECT guest_id, host_id FROM app.conversations
		 WHERE id = $1 AND (guest_id = $2 OR host_id = $2)
		 FOR UPDATE`,
		conversationID, actorID).Scan(&participants.GuestID, &participants.HostID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Participants{}, ErrConversationNotFound
	}
	if err != nil {
		return Participants{}, fmt.Errorf("select conversation: %w", err)
	}
	return participants, nil
}

func requireMessageIn(ctx context.Context, tx pgx.Tx, conversationID, messageID uuid.UUID) error {
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM app.messages WHERE id = $1 AND conversation_id = $2)`,
		messageID, conversationID).Scan(&exists); err != nil {
		return fmt.Errorf("check message: %w", err)
	}
	if !exists {
		return ErrMessageNotFound
	}
	return nil
}

func selectMessage(ctx context.Context, tx pgx.Tx, conversationID, messageID uuid.UUID) (Message, error) {
	message, err := scanMessage(tx.QueryRow(ctx,
		`SELECT `+messageColumns+` `+messageFrom+` WHERE m.id = $1 AND m.conversation_id = $2`,
		messageID, conversationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, ErrMessageNotFound
	}
	if err != nil {
		return Message{}, fmt.Errorf("select message: %w", err)
	}
	return message, nil
}

func scanMessage(row pgx.Row) (Message, error) {
	var (
		message   Message
		replyID   *uuid.UUID
		replyRole *string
		replyBody *string
		emoji     []string
		roles     []string
	)
	if err := row.Scan(
		&message.ID, &message.ConversationID, &message.SenderID, &message.SenderRole,
		&message.Body, &message.CreatedAt, &message.BookingID,
		&replyID, &replyRole, &replyBody, &emoji, &roles,
	); err != nil {
		return Message{}, err
	}
	if replyID != nil && replyRole != nil && replyBody != nil {
		message.ReplyTo = &Reply{ID: *replyID, SenderRole: Role(*replyRole), Body: *replyBody}
	}
	message.Reactions = make([]Reaction, 0, len(emoji))
	for index, value := range emoji {
		if index < len(roles) {
			message.Reactions = append(message.Reactions, Reaction{Emoji: value, Role: Role(roles[index])})
		}
	}
	return message, nil
}

func (store *PostgresMessageStore) Hide(ctx context.Context, actorID, conversationID uuid.UUID, now time.Time) error {
	return db.WithUserTx(ctx, store.DB, actorID.String(), func(tx pgx.Tx) error {
		participants, err := lockParticipants(ctx, tx, actorID, conversationID)
		if err != nil {
			return err
		}
		var stayOpen bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM app.conversations c JOIN app.bookings b ON `+stayStillOpen+` WHERE c.id = $1)`,
			conversationID).Scan(&stayOpen); err != nil {
			return fmt.Errorf("check open stay: %w", err)
		}
		if stayOpen {
			return ErrStayStillOpen
		}
		hiddenColumn, readColumn := "guest_hidden_at", "guest_read_at"
		if participants.HostID == actorID {
			hiddenColumn, readColumn = "host_hidden_at", "host_read_at"
		}
		if _, err := tx.Exec(ctx,
			`UPDATE app.conversations SET `+hiddenColumn+` = $2, `+readColumn+` = $2 WHERE id = $1`,
			conversationID, now); err != nil {
			return fmt.Errorf("hide conversation: %w", err)
		}
		return nil
	})
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
		&summary.LastMessageAt, &summary.LastMessagePreview, &summary.UnreadCount, &summary.CanDelete,
	)
	return summary, err
}
