package storage

import (
	"database/sql"
	"fmt"
	"time"
)

// SlackThreadSummary holds a cached summary for a Slack thread.
type SlackThreadSummary struct {
	ID           int64
	ChannelID    string
	ThreadTS     string
	Summary      string
	MessageCount int
	SummarizedAt time.Time
}

// GetSlackThreadSummary returns a cached thread summary, or nil,nil if not found.
func GetSlackThreadSummary(db *sql.DB, channelID, threadTS string) (*SlackThreadSummary, error) {
	var s SlackThreadSummary
	err := db.QueryRow(
		`SELECT id, channel_id, thread_ts, summary, message_count, summarized_at
		 FROM slack_thread_summaries WHERE channel_id = ? AND thread_ts = ?`,
		channelID, threadTS,
	).Scan(&s.ID, &s.ChannelID, &s.ThreadTS, &s.Summary, &s.MessageCount, &s.SummarizedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get slack thread summary: %w", err)
	}
	return &s, nil
}

// UpsertSlackThreadSummary inserts or updates a thread summary.
func UpsertSlackThreadSummary(db *sql.DB, channelID, threadTS, summary string, msgCount int) error {
	_, err := db.Exec(
		`INSERT INTO slack_thread_summaries (channel_id, thread_ts, summary, message_count, summarized_at)
		 VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(channel_id, thread_ts) DO UPDATE SET
		   summary = excluded.summary,
		   message_count = excluded.message_count,
		   summarized_at = CURRENT_TIMESTAMP`,
		channelID, threadTS, summary, msgCount,
	)
	if err != nil {
		return fmt.Errorf("upsert slack thread summary: %w", err)
	}
	return nil
}
