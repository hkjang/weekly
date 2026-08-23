package app

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Evidence lineage.
//
// A report item says what happened. Until now nothing said where it came from:
// the Confluence pages behind a candidate and the slide numbers behind an
// imported item were both discarded the moment the item was written, so the
// question a reader actually has six months later — 이 문장은 무엇을 근거로
// 적힌 것인가 — had no answer anywhere.
//
// One shape for every origin. Per-connector tables would make "how much
// evidence is behind this" a different query for each kind, and the point of
// the model is that a reader can compare.

const (
	sourceManual     = "MANUAL"
	sourceConfluence = "CONFLUENCE"
	sourcePPTX       = "PPTX"
	sourceAIText     = "AI_TEXT"
)

type itemSource struct {
	Kind       string     `json:"kind"`
	Reference  string     `json:"reference,omitempty"`
	Title      string     `json:"title,omitempty"`
	URL        string     `json:"url,omitempty"`
	Detail     string     `json:"detail,omitempty"`
	OccurredAt *time.Time `json:"occurredAt,omitempty"`
}

// recordItemSources writes provenance for one report item. Called at the moment
// the item is created from something, which is the only moment the origin is
// still known.
func recordItemSources(ctx context.Context, tx pgx.Tx, reportItemID int64, sources []itemSource) error {
	for _, source := range sources {
		if strings.TrimSpace(source.Kind) == "" {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO report_item_sources(report_item_id,kind,reference,title,url,detail,occurred_at)
			VALUES($1,$2,$3,$4,$5,$6,$7)`,
			reportItemID, source.Kind, trimRunes(source.Reference, 240), trimRunes(source.Title, 240),
			source.URL, source.Detail, source.OccurredAt); err != nil {
			return err
		}
	}
	return nil
}

// sourcesForReport reads the evidence behind every item of one report, keyed by
// item so the caller does not have to regroup it.
func (a *App) sourcesForReport(ctx context.Context, reportID int64) (map[int64][]itemSource, error) {
	grouped := map[int64][]itemSource{}
	rows, err := a.db.Query(ctx, `SELECT s.report_item_id, s.kind, s.reference, s.title, s.url, s.detail, s.occurred_at
		FROM report_item_sources s JOIN report_items i ON i.id = s.report_item_id
		WHERE i.report_id = $1 ORDER BY s.report_item_id, s.id`, reportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var itemID int64
		var source itemSource
		if err := rows.Scan(&itemID, &source.Kind, &source.Reference, &source.Title,
			&source.URL, &source.Detail, &source.OccurredAt); err != nil {
			return nil, err
		}
		grouped[itemID] = append(grouped[itemID], source)
	}
	return grouped, rows.Err()
}
