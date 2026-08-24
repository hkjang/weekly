package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const confluenceAdvisoryLock int64 = 879362041

type confluenceSyncCounters struct {
	PagesScanned      int
	PagesChanged      int
	CandidatesCreated int
	PagesFailed       int
}

func (a *App) wakeConfluenceWorker() {
	select {
	case a.confluenceWake <- struct{}{}:
	default:
	}
}

func (a *App) confluenceWorker(ctx context.Context) {
	_, _ = a.db.Exec(ctx, `UPDATE confluence_sync_state SET status='FAILED',error_message='서비스 재시작으로 이전 동기화가 중단되었습니다.',current_started_at=NULL,updated_at=now() WHERE system_type='CONFLUENCE' AND status='RUNNING'`)
	initial := time.NewTimer(2 * time.Second)
	ticker := time.NewTicker(time.Minute)
	defer initial.Stop()
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-initial.C:
			a.runConfluenceSyncIfDue(ctx, true)
		case <-ticker.C:
			a.runConfluenceSyncIfDue(ctx, false)
		case <-a.confluenceWake:
			a.runConfluenceSyncIfDue(ctx, true)
		}
	}
}

func (a *App) runConfluenceSyncIfDue(ctx context.Context, force bool) {
	cfg, err := a.loadConfluenceSettings(ctx)
	if err != nil || !cfg.Enabled {
		return
	}
	if !force {
		var lastAttempt *time.Time
		_ = a.db.QueryRow(ctx, `SELECT last_attempt_at FROM confluence_sync_state WHERE system_type='CONFLUENCE'`).Scan(&lastAttempt)
		interval := time.Duration(cfg.SyncIntervalMinutes) * time.Minute
		if interval < 5*time.Minute {
			interval = 5 * time.Minute
		}
		if lastAttempt != nil && time.Since(*lastAttempt) < interval {
			return
		}
	}
	if err := a.runConfluenceSync(ctx, cfg); err != nil {
		a.logger.Warn("Confluence synchronization failed", "error", safeConfluenceError(err))
	}
}

func (a *App) runConfluenceSync(ctx context.Context, cfg confluenceSettings) error {
	connection, err := a.db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer connection.Release()
	var locked bool
	if err := connection.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, confluenceAdvisoryLock).Scan(&locked); err != nil || !locked {
		return err
	}
	defer func() {
		_, _ = connection.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, confluenceAdvisoryLock)
	}()

	client, err := cfg.client()
	if err != nil {
		a.finishConfluenceSync(ctx, "FAILED", err.Error(), confluenceSyncCounters{}, false)
		return err
	}
	_, err = a.db.Exec(ctx, `UPDATE confluence_sync_state SET status='RUNNING',last_attempt_at=now(),current_started_at=now(),error_message='',pages_scanned=0,pages_changed=0,candidates_created=0,pages_failed=0,updated_at=now() WHERE system_type='CONFLUENCE'`)
	if err != nil {
		return err
	}

	var lastSuccess *time.Time
	_ = a.db.QueryRow(ctx, `SELECT last_success_at FROM confluence_sync_state WHERE system_type='CONFLUENCE'`).Scan(&lastSuccess)
	since := time.Now().AddDate(0, 0, -cfg.LookbackDays)
	if lastSuccess != nil {
		since = lastSuccess.Add(-2 * time.Minute)
	}

	counters := confluenceSyncCounters{}
	pages := make([]ConfluencePage, 0)
	querySince := since.In(a.serviceLocation(ctx))
	for start := 0; ; {
		result, searchErr := client.SearchChangedPages(ctx, querySince, start, cfg.BatchSize)
		if searchErr != nil {
			a.recordConfluenceError(ctx, "", "SEARCH", confluenceErrorStatus(searchErr), searchErr)
			a.finishConfluenceSync(ctx, "FAILED", safeConfluenceError(searchErr), counters, false)
			return searchErr
		}
		counters.PagesScanned += result.Size
		for _, page := range result.Pages {
			if cfg.allowsSpace(page.SpaceKey) && (page.Status == "" || page.Status == "CURRENT") {
				pages = append(pages, page)
			}
		}
		if result.Size == 0 || result.Size < result.Limit {
			break
		}
		start += result.Size
		if start >= 10000 {
			err = errors.New("Confluence search exceeded the 10000 page safety limit")
			a.recordConfluenceError(ctx, "", "SEARCH", 0, err)
			a.finishConfluenceSync(ctx, "FAILED", err.Error(), counters, false)
			return err
		}
	}

	pageDBIDs := map[string]int64{}
	actorNames := map[string]bool{}
	for _, page := range pages {
		pageID, changed, upsertErr := a.upsertConfluencePage(ctx, page)
		if upsertErr != nil {
			counters.PagesFailed++
			a.recordConfluenceError(ctx, page.ID, "METADATA_STORE", 0, upsertErr)
			continue
		}
		pageDBIDs[page.ID] = pageID
		if changed {
			counters.PagesChanged++
		}
		if page.CreatorUsername != "" {
			actorNames[strings.ToLower(page.CreatorUsername)] = true
		}
		if page.LastModifierUsername != "" {
			actorNames[strings.ToLower(page.LastModifierUsername)] = true
		}
	}

	mappings, mappingErr := a.resolveConfluenceUsers(ctx, actorNames, cfg)
	if mappingErr != nil {
		counters.PagesFailed++
		a.recordConfluenceError(ctx, "", "IDENTITY_MAPPING", 0, mappingErr)
	}
	activities := a.buildConfluenceActivities(ctx, pages, pageDBIDs, mappings, since, cfg)
	groupsByOwner := map[string][]confluenceActivity{}
	for _, activity := range activities {
		needed, needErr := a.confluenceActivityNeedsAnalysis(ctx, activity)
		if needErr != nil {
			counters.PagesFailed++
			a.recordConfluenceError(ctx, activity.Page.ID, "DEDUPLICATION", 0, needErr)
			continue
		}
		if !needed {
			continue
		}
		activity.RuleScore = scoreConfluenceActivity(activity, cfg)
		if activity.RuleScore < cfg.AIReviewMinimumScore {
			continue
		}
		key := fmt.Sprintf("%d:%s", activity.UserID, activity.WeekStart)
		groupsByOwner[key] = append(groupsByOwner[key], activity)
	}

	var aiCfg *aiConfiguration
	if cfg.AIEnabled {
		if loaded, aiErr := a.aiConfig(ctx, true); aiErr == nil {
			aiCfg = &loaded
		} else {
			a.recordConfluenceError(ctx, "", "AI_CONFIGURATION", 0, aiErr)
		}
	}
	ownerKeys := make([]string, 0, len(groupsByOwner))
	for key := range groupsByOwner {
		ownerKeys = append(ownerKeys, key)
	}
	sort.Strings(ownerKeys)
	for _, key := range ownerKeys {
		ownerActivities := groupsByOwner[key]
		if aiCfg != nil && cfg.AnalyzeBody {
			ownerActivities = a.loadConfluenceBodyPreviews(ctx, client, ownerActivities, aiCfg.MaxInput)
		}
		if len(ownerActivities) == 0 {
			continue
		}
		groups := make([]confluenceCandidateGroup, 0)
		decided := map[string]bool{}
		fallbackThreshold := cfg.AIReviewMinimumScore
		if aiCfg != nil {
			classified, classifierDecisions, classifyErr := callConfluenceClassifier(ctx, *aiCfg, ownerActivities)
			if classifyErr != nil {
				a.recordConfluenceError(ctx, "", "AI_CLASSIFY", 0, classifyErr)
			} else {
				groups = append(groups, classified...)
				decided = classifierDecisions
				fallbackThreshold = cfg.MinimumScore
				for pageID, included := range decided {
					if included {
						continue
					}
					for _, activity := range ownerActivities {
						if activity.Page.ID == pageID {
							if markErr := a.markConfluenceActivityExcluded(ctx, activity); markErr != nil {
								counters.PagesFailed++
								a.recordConfluenceError(ctx, pageID, "EXCLUSION", 0, markErr)
							}
							break
						}
					}
				}
			}
		}
		fallback := make([]confluenceActivity, 0)
		for _, activity := range ownerActivities {
			if _, wasDecided := decided[activity.Page.ID]; !wasDecided && activity.RuleScore >= fallbackThreshold {
				fallback = append(fallback, activity)
			}
		}
		groups = append(groups, deterministicConfluenceGroups(fallback)...)
		for _, group := range groups {
			created, processErr := a.processConfluenceGroup(ctx, client, cfg, aiCfg, group)
			if processErr != nil {
				counters.PagesFailed++
				a.recordConfluenceError(ctx, firstGroupPageID(group), "CANDIDATE", 0, processErr)
				continue
			}
			if created {
				counters.CandidatesCreated++
			}
		}
	}

	status := "SUCCESS"
	completed := true
	message := ""
	if counters.PagesFailed > 0 {
		status = "PARTIAL"
		completed = false
		message = fmt.Sprintf("%d개 Page 처리에 실패했습니다.", counters.PagesFailed)
	}
	a.finishConfluenceSync(ctx, status, message, counters, completed)
	a.auditSystem(ctx, "confluence.sync", "confluence", "global", map[string]any{
		"status": status, "pagesScanned": counters.PagesScanned, "pagesChanged": counters.PagesChanged,
		"candidatesCreated": counters.CandidatesCreated, "pagesFailed": counters.PagesFailed,
	})
	return nil
}

func (a *App) markConfluenceActivityExcluded(ctx context.Context, activity confluenceActivity) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	command, err := tx.Exec(ctx, `UPDATE candidate_sources cs SET page_version=$4,activity_type=$5,source_updated_at=$6,updated_at=now()
		FROM report_candidates c WHERE c.id=cs.candidate_id AND cs.confluence_page_id=$1 AND c.user_id=$2 AND c.week_start=$3`,
		activity.PageDBID, activity.UserID, activity.WeekStart, activity.Page.Version, activity.ActivityType, activity.Page.UpdatedAt)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		var candidateID int64
		err = tx.QueryRow(ctx, `INSERT INTO report_candidates(user_id,week_start,normalized_title,category,current_result,confidence,rule_score,status)
			VALUES($1,$2,$3,'Confluence','',0,$4,'REMOVED') RETURNING id`, activity.UserID, activity.WeekStart, normalizeConfluenceTitle(activity.Page.Title), activity.RuleScore).Scan(&candidateID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO candidate_sources(candidate_id,confluence_page_id,page_version,activity_type,source_updated_at)
			VALUES($1,$2,$3,$4,$5)`, candidateID, activity.PageDBID, activity.Page.Version, activity.ActivityType, activity.Page.UpdatedAt)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (a *App) confluenceActivityNeedsAnalysis(ctx context.Context, activity confluenceActivity) (bool, error) {
	var status string
	var userEdited bool
	var version int
	err := a.db.QueryRow(ctx, `SELECT c.status,c.user_edited,cs.page_version FROM candidate_sources cs JOIN report_candidates c ON c.id=cs.candidate_id
		WHERE cs.confluence_page_id=$1 AND c.user_id=$2 AND c.week_start=$3 ORDER BY c.id DESC LIMIT 1`, activity.PageDBID, activity.UserID, activity.WeekStart).Scan(&status, &userEdited, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if status != "DETECTED" || userEdited || version >= activity.Page.Version {
		return false, nil
	}
	return true, nil
}

func (a *App) upsertConfluencePage(ctx context.Context, page ConfluencePage) (int64, bool, error) {
	var previousVersion int
	var id int64
	err := a.db.QueryRow(ctx, `SELECT id,page_version FROM confluence_pages WHERE page_id=$1`, page.ID).Scan(&id, &previousVersion)
	changed := err != nil || previousVersion != page.Version
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	var createdAt any
	if !page.CreatedAt.IsZero() {
		createdAt = page.CreatedAt
	}
	var updatedAt any
	if !page.UpdatedAt.IsZero() {
		updatedAt = page.UpdatedAt
	}
	err = a.db.QueryRow(ctx, `INSERT INTO confluence_pages(page_id,content_type,status,space_key,title,creator_username,last_modifier_username,created_at_source,updated_at_source,page_url,title_hash,page_version,last_synced_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,now())
		ON CONFLICT(page_id) DO UPDATE SET content_type=EXCLUDED.content_type,status=EXCLUDED.status,space_key=EXCLUDED.space_key,title=EXCLUDED.title,
		creator_username=EXCLUDED.creator_username,last_modifier_username=EXCLUDED.last_modifier_username,created_at_source=EXCLUDED.created_at_source,
		updated_at_source=EXCLUDED.updated_at_source,page_url=EXCLUDED.page_url,title_hash=EXCLUDED.title_hash,page_version=EXCLUDED.page_version,last_error='',last_synced_at=now(),updated_at=now()
		RETURNING id`, page.ID, page.Type, page.Status, page.SpaceKey, page.Title, page.CreatorUsername, page.LastModifierUsername, createdAt, updatedAt, page.WebURL, confluenceTextHash(page.Title), page.Version).Scan(&id)
	return id, changed, err
}

func (a *App) resolveConfluenceUsers(ctx context.Context, actorNames map[string]bool, cfg confluenceSettings) (map[string]int64, error) {
	result := map[string]int64{}
	blockedExternal := map[string]bool{}
	reservedUsers := map[int64]bool{}
	rows, err := a.db.Query(ctx, `SELECT user_id,lower(external_username),active FROM user_external_accounts WHERE system_type='CONFLUENCE'`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var userID int64
		var username string
		var active bool
		if err := rows.Scan(&userID, &username, &active); err != nil {
			rows.Close()
			return result, err
		}
		blockedExternal[username] = true
		reservedUsers[userID] = true
		if active {
			result[username] = userID
		}
	}
	rows.Close()

	type localUser struct {
		id                   int64
		username, emailLocal string
	}
	users := make([]localUser, 0)
	rows, err = a.db.Query(ctx, `SELECT id,lower(username),lower(coalesce(email,'')) FROM users WHERE active=true ORDER BY id`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var user localUser
		var email string
		if err := rows.Scan(&user.id, &user.username, &email); err != nil {
			rows.Close()
			return result, err
		}
		if at := strings.IndexByte(email, '@'); at > 0 {
			user.emailLocal = email[:at]
		}
		users = append(users, user)
	}
	rows.Close()

	emailCandidates := map[string][]int64{}
	usernameCandidates := map[string][]int64{}
	for _, user := range users {
		if user.emailLocal != "" {
			emailCandidates[user.emailLocal] = append(emailCandidates[user.emailLocal], user.id)
		}
		usernameCandidates[user.username] = append(usernameCandidates[user.username], user.id)
	}
	names := make([]string, 0, len(actorNames))
	for name := range actorNames {
		names = append(names, strings.ToLower(name))
	}
	sort.Strings(names)
	for _, external := range names {
		if _, ok := result[external]; ok || blockedExternal[external] {
			continue
		}
		var userID int64
		source := ""
		if cfg.AutoMapEmailLocalpart && len(emailCandidates[external]) == 1 {
			userID = emailCandidates[external][0]
			source = "EMAIL_LOCALPART"
		}
		if userID == 0 && cfg.AutoMapUsername && len(usernameCandidates[external]) == 1 {
			userID = usernameCandidates[external][0]
			source = "USERNAME"
		}
		if userID == 0 || reservedUsers[userID] {
			continue
		}
		command, insertErr := a.db.Exec(ctx, `INSERT INTO user_external_accounts(user_id,system_type,external_username,mapping_source,active)
			VALUES($1,'CONFLUENCE',$2,$3,true) ON CONFLICT DO NOTHING`, userID, external, source)
		if insertErr != nil {
			return result, insertErr
		}
		if command.RowsAffected() == 1 {
			result[external] = userID
			reservedUsers[userID] = true
			a.auditSystem(ctx, "confluence.mapping.auto", "user", fmt.Sprintf("%d", userID), map[string]any{"externalUsername": external, "source": source})
		}
	}
	return result, nil
}

func (a *App) buildConfluenceActivities(ctx context.Context, pages []ConfluencePage, pageDBIDs map[string]int64, mappings map[string]int64, since time.Time, cfg confluenceSettings) []confluenceActivity {
	location := a.serviceLocation(ctx)
	weekStartSetting := a.setting(ctx, "workflow.week_start", "MONDAY")
	result := make([]confluenceActivity, 0)
	for _, page := range pages {
		pageDBID, ok := pageDBIDs[page.ID]
		if !ok {
			continue
		}
		creatorID := mappings[strings.ToLower(page.CreatorUsername)]
		modifierID := mappings[strings.ToLower(page.LastModifierUsername)]
		creatorRecent := creatorID > 0 && !page.CreatedAt.IsZero() && !page.CreatedAt.Before(since)
		modifierRecent := modifierID > 0 && !page.UpdatedAt.IsZero() && !page.UpdatedAt.Before(since)
		creatorWeek := ""
		modifierWeek := ""
		if creatorRecent {
			creatorWeek = currentWeekStart(page.CreatedAt.In(location), weekStartSetting).Format("2006-01-02")
		}
		if modifierRecent {
			modifierWeek = currentWeekStart(page.UpdatedAt.In(location), weekStartSetting).Format("2006-01-02")
		}
		if creatorRecent && modifierRecent && creatorID == modifierID && creatorWeek == modifierWeek {
			result = append(result, confluenceActivity{Page: page, PageDBID: pageDBID, UserID: creatorID, WeekStart: creatorWeek, ActivityType: "CREATED_AND_MODIFIED"})
			continue
		}
		if creatorRecent {
			result = append(result, confluenceActivity{Page: page, PageDBID: pageDBID, UserID: creatorID, WeekStart: creatorWeek, ActivityType: "CREATED"})
		}
		if modifierRecent {
			result = append(result, confluenceActivity{Page: page, PageDBID: pageDBID, UserID: modifierID, WeekStart: modifierWeek, ActivityType: "MODIFIED"})
		}
	}
	return result
}

func (a *App) processConfluenceGroup(ctx context.Context, client ConfluenceClient, cfg confluenceSettings, aiCfg *aiConfiguration, group confluenceCandidateGroup) (bool, error) {
	if len(group.Activities) == 0 {
		return false, nil
	}
	filtered := make([]confluenceActivity, 0, len(group.Activities))
	for _, activity := range group.Activities {
		var status string
		var userEdited bool
		var version int
		err := a.db.QueryRow(ctx, `SELECT c.status,c.user_edited,cs.page_version FROM candidate_sources cs JOIN report_candidates c ON c.id=cs.candidate_id
			WHERE cs.confluence_page_id=$1 AND c.user_id=$2 AND c.week_start=$3 ORDER BY c.id DESC LIMIT 1`, activity.PageDBID, activity.UserID, activity.WeekStart).Scan(&status, &userEdited, &version)
		if err == nil {
			if status != "DETECTED" || userEdited || version >= activity.Page.Version {
				continue
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
		filtered = append(filtered, activity)
	}
	if len(filtered) == 0 {
		return false, nil
	}
	group.Activities = filtered
	bodies := map[string]string{}
	if cfg.AnalyzeBody {
		available := make([]confluenceActivity, 0, len(filtered))
		maximum := 12000
		if aiCfg != nil {
			budget := aiCfg.MaxInput - 8000
			if budget < 1000*len(filtered) {
				budget = 1000 * len(filtered)
			}
			if budget/len(filtered) < maximum {
				maximum = budget / len(filtered)
			}
		}
		if maximum < 1000 {
			maximum = 1000
		}
		for _, activity := range filtered {
			if activity.BodyLoaded {
				bodies[activity.Page.ID] = trimRunes(activity.BodyText, maximum)
				available = append(available, activity)
				continue
			}
			if activity.BodyAttempted {
				available = append(available, activity)
				continue
			}
			body, err := client.GetPageBody(ctx, activity.Page.ID)
			if err != nil {
				statusCode := confluenceErrorStatus(err)
				a.recordConfluenceError(ctx, activity.Page.ID, "BODY", statusCode, err)
				if statusCode == http.StatusNotFound {
					_, _ = a.db.Exec(ctx, `UPDATE confluence_pages SET status='DELETED',last_error='HTTP 404',updated_at=now() WHERE id=$1`, activity.PageDBID)
					if markErr := a.markConfluenceActivityExcluded(ctx, activity); markErr != nil {
						return false, markErr
					}
					continue
				}
				if statusCode == http.StatusForbidden {
					available = append(available, activity)
					continue
				}
				return false, err
			}
			if !confluenceBodyMatchesActivity(body, activity) {
				a.recordConfluenceError(ctx, activity.Page.ID, "BODY_VERSION_CHANGED", 0, fmt.Errorf("Confluence page changed during synchronization: metadata version %d, body version %d", activity.Page.Version, body.Version))
				continue
			}
			cleaned := cleanConfluenceStorage(body.Storage, maximum)
			bodies[activity.Page.ID] = cleaned
			available = append(available, activity)
			_, _ = a.db.Exec(ctx, `UPDATE confluence_pages SET body_hash=$2,last_error='',updated_at=now() WHERE id=$1`, activity.PageDBID, confluenceTextHash(cleaned))
		}
		group.Activities = available
		if len(group.Activities) == 0 {
			return false, nil
		}
	}
	summary := fallbackConfluenceSummary(group)
	if aiCfg != nil {
		if generated, err := callConfluenceSummarizer(ctx, *aiCfg, group, bodies); err == nil {
			summary = generated
		} else {
			a.recordConfluenceError(ctx, firstGroupPageID(group), "AI_SUMMARY", 0, err)
		}
	}
	if strings.TrimSpace(summary.CurrentResult) == "" {
		summary.CurrentResult = fallbackConfluenceSummary(group).CurrentResult
	}
	return a.upsertReportCandidate(ctx, group, summary)
}

func (a *App) loadConfluenceBodyPreviews(ctx context.Context, client ConfluenceClient, activities []confluenceActivity, maximumInput int) []confluenceActivity {
	if len(activities) == 0 {
		return activities
	}
	maximum := 3000
	if budget := (maximumInput - 6000) / len(activities); budget < maximum {
		maximum = budget
	}
	if maximum < 500 {
		maximum = 500
	}
	result := make([]confluenceActivity, 0, len(activities))
	for _, activity := range activities {
		body, err := client.GetPageBody(ctx, activity.Page.ID)
		activity.BodyAttempted = true
		if err != nil {
			statusCode := confluenceErrorStatus(err)
			a.recordConfluenceError(ctx, activity.Page.ID, "BODY_PREVIEW", statusCode, err)
			if statusCode == http.StatusNotFound {
				_, _ = a.db.Exec(ctx, `UPDATE confluence_pages SET status='DELETED',last_error='HTTP 404',updated_at=now() WHERE id=$1`, activity.PageDBID)
				if markErr := a.markConfluenceActivityExcluded(ctx, activity); markErr != nil {
					a.recordConfluenceError(ctx, activity.Page.ID, "EXCLUSION", 0, markErr)
				}
				continue
			}
			result = append(result, activity)
			continue
		}
		if !confluenceBodyMatchesActivity(body, activity) {
			a.recordConfluenceError(ctx, activity.Page.ID, "BODY_VERSION_CHANGED", 0, fmt.Errorf("Confluence page changed during synchronization: metadata version %d, body version %d", activity.Page.Version, body.Version))
			continue
		}
		activity.BodyText = cleanConfluenceStorage(body.Storage, maximum)
		activity.BodyLoaded = true
		_, _ = a.db.Exec(ctx, `UPDATE confluence_pages SET body_hash=$2,last_error='',updated_at=now() WHERE id=$1`, activity.PageDBID, confluenceTextHash(activity.BodyText))
		result = append(result, activity)
	}
	return result
}

func confluenceBodyMatchesActivity(body *ConfluencePageBody, activity confluenceActivity) bool {
	if body == nil {
		return false
	}
	if body.PageID != "" && body.PageID != activity.Page.ID {
		return false
	}
	return body.Version <= 0 || activity.Page.Version <= 0 || body.Version == activity.Page.Version
}

func (a *App) upsertReportCandidate(ctx context.Context, group confluenceCandidateGroup, summary confluenceSummaryResult) (bool, error) {
	owner := group.Activities[0]
	maxScore := owner.RuleScore
	for _, activity := range group.Activities[1:] {
		if activity.RuleScore > maxScore {
			maxScore = activity.RuleScore
		}
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var candidateID int64
	var userEdited bool
	var existingCurrent, existingNext, existingIssue string
	for _, activity := range group.Activities {
		err = tx.QueryRow(ctx, `SELECT c.id,c.user_edited,c.current_result,c.next_plan,c.issue FROM candidate_sources cs JOIN report_candidates c ON c.id=cs.candidate_id
			WHERE cs.confluence_page_id=$1 AND c.user_id=$2 AND c.week_start=$3 AND c.status='DETECTED' ORDER BY c.id DESC LIMIT 1`, activity.PageDBID, owner.UserID, owner.WeekStart).Scan(&candidateID, &userEdited, &existingCurrent, &existingNext, &existingIssue)
		if err == nil {
			break
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
	}
	if candidateID == 0 {
		rows, queryErr := tx.Query(ctx, `SELECT id,normalized_title,user_edited,current_result,next_plan,issue FROM report_candidates WHERE user_id=$1 AND week_start=$2 AND status='DETECTED' ORDER BY id`, owner.UserID, owner.WeekStart)
		if queryErr != nil {
			return false, queryErr
		}
		key := candidateTitleKey(group.Title)
		for rows.Next() {
			var id int64
			var title string
			var edited bool
			var currentResult, nextPlan, issue string
			if scanErr := rows.Scan(&id, &title, &edited, &currentResult, &nextPlan, &issue); scanErr != nil {
				rows.Close()
				return false, scanErr
			}
			if candidateTitleKey(title) == key {
				candidateID, userEdited = id, edited
				existingCurrent, existingNext, existingIssue = currentResult, nextPlan, issue
				break
			}
		}
		rows.Close()
	}
	created := candidateID == 0
	confidence := summary.Confidence
	if group.Confidence > confidence {
		confidence = group.Confidence
	}
	if created {
		err = tx.QueryRow(ctx, `INSERT INTO report_candidates(user_id,week_start,normalized_title,category,current_result,next_plan,issue,confidence,rule_score)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`, owner.UserID, owner.WeekStart, group.Title, group.Category, summary.CurrentResult, summary.NextPlan, summary.Issue, confidence, maxScore).Scan(&candidateID)
		if err != nil {
			return false, err
		}
	} else if !userEdited {
		_, err = tx.Exec(ctx, `UPDATE report_candidates SET normalized_title=$2,category=$3,current_result=$4,next_plan=$5,issue=$6,
			confidence=greatest(confidence,$7),rule_score=greatest(rule_score,$8),updated_at=now() WHERE id=$1`, candidateID, group.Title, group.Category,
			mergeUniqueLines(existingCurrent, summary.CurrentResult), mergeUniqueLines(existingNext, summary.NextPlan), mergeUniqueLines(existingIssue, summary.Issue), confidence, maxScore)
		if err != nil {
			return false, err
		}
	}
	for _, activity := range group.Activities {
		_, err = tx.Exec(ctx, `INSERT INTO candidate_sources(candidate_id,confluence_page_id,page_version,activity_type,source_updated_at)
			VALUES($1,$2,$3,$4,$5) ON CONFLICT(candidate_id,confluence_page_id) DO UPDATE SET page_version=EXCLUDED.page_version,
			activity_type=EXCLUDED.activity_type,source_updated_at=EXCLUDED.source_updated_at,updated_at=now()`, candidateID, activity.PageDBID, activity.Page.Version, activity.ActivityType, activity.Page.UpdatedAt)
		if err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return created, nil
}

func firstGroupPageID(group confluenceCandidateGroup) string {
	if len(group.Activities) == 0 {
		return ""
	}
	return group.Activities[0].Page.ID
}

func (a *App) finishConfluenceSync(ctx context.Context, status, message string, counters confluenceSyncCounters, success bool) {
	if success {
		_, _ = a.db.Exec(ctx, `UPDATE confluence_sync_state SET status=$1,last_success_at=now(),current_started_at=NULL,error_message=$2,pages_scanned=$3,pages_changed=$4,candidates_created=$5,pages_failed=$6,updated_at=now() WHERE system_type='CONFLUENCE'`, status, trimRunes(message, 2000), counters.PagesScanned, counters.PagesChanged, counters.CandidatesCreated, counters.PagesFailed)
	} else {
		_, _ = a.db.Exec(ctx, `UPDATE confluence_sync_state SET status=$1,current_started_at=NULL,error_message=$2,pages_scanned=$3,pages_changed=$4,candidates_created=$5,pages_failed=$6,updated_at=now() WHERE system_type='CONFLUENCE'`, status, trimRunes(message, 2000), counters.PagesScanned, counters.PagesChanged, counters.CandidatesCreated, counters.PagesFailed)
	}
}

func (a *App) recordConfluenceError(ctx context.Context, pageID, phase string, statusCode int, err error) {
	if err == nil {
		return
	}
	_, _ = a.db.Exec(ctx, `INSERT INTO confluence_sync_errors(page_id,phase,status_code,error_message) VALUES($1,$2,$3,$4)`, nullableString(pageID), phase, nullableInteger(statusCode), trimRunes(safeConfluenceError(err), 2000))
	_, _ = a.db.Exec(ctx, `DELETE FROM confluence_sync_errors WHERE id IN (SELECT id FROM confluence_sync_errors ORDER BY created_at DESC OFFSET 500)`)
}

func confluenceErrorStatus(err error) int {
	var httpErr *ConfluenceHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode
	}
	return 0
}

// safeConfluenceError turns a connection failure into something the person
// reading it can act on.
//
// It used to hand back either "Confluence HTTP 401" — accurate, and no help in
// deciding what to change — or the raw Go error, which on a wrong address meant
// the administrator screen showed
// `Get "http://host/confluence/rest/api/content/search?cql=type+%3D+page..."`,
// and on a proxy sitting in front of Confluence meant
// `invalid character '<' looking for beginning of value`. Neither says what is
// wrong, and the second is an internal API path on a settings screen.
//
// The raw error still goes to the log, where whoever is debugging wants it.
func safeConfluenceError(err error) string {
	if err == nil {
		return ""
	}
	var httpErr *ConfluenceHTTPError
	if errors.As(err, &httpErr) {
		switch {
		case httpErr.StatusCode == http.StatusUnauthorized:
			return "Confluence가 인증을 거부했습니다(HTTP 401). 연동 전용 계정의 아이디와 비밀번호를 확인하세요."
		case httpErr.StatusCode == http.StatusForbidden:
			return "Confluence가 접근을 거부했습니다(HTTP 403). 연동 계정이 대상 Space를 볼 수 있는지 확인하세요."
		case httpErr.StatusCode == http.StatusNotFound:
			return "Confluence에서 REST 경로를 찾지 못했습니다(HTTP 404). Base URL이 Confluence 루트(예: https://confluence.internal/confluence)인지 확인하세요."
		case httpErr.StatusCode >= 500:
			return fmt.Sprintf("Confluence가 오류로 응답했습니다(HTTP %d). Confluence 쪽 문제이므로 이 설정을 바꿔도 해결되지 않습니다.", httpErr.StatusCode)
		}
		return fmt.Sprintf("Confluence가 HTTP %d로 응답했습니다.", httpErr.StatusCode)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "Confluence 요청 시간이 초과되었습니다. 주소와 사내망 경로를 확인하세요."
	}
	lowered := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lowered, "looking for beginning of value") || strings.Contains(lowered, "decode confluence response"):
		return "Confluence 주소가 JSON이 아닌 응답을 돌려줬습니다. 프록시나 로그인 페이지가 앞에 있는지, Base URL이 맞는지 확인하세요."
	case strings.Contains(lowered, "no such host") || strings.Contains(lowered, "connection refused") || strings.Contains(lowered, "dial "):
		return "Confluence 주소에 연결하지 못했습니다. Base URL과 사내망 접근 경로를 확인하세요."
	case strings.Contains(lowered, "certificate") || strings.Contains(lowered, "x509"):
		return "Confluence 인증서를 검증하지 못했습니다. 사내 CA가 이 서버에 설치돼 있는지 확인하세요."
	}
	return "Confluence에 연결하지 못했습니다. 서버 로그에서 자세한 원인을 확인하세요."
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullableInteger(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func (a *App) auditSystem(ctx context.Context, action, resourceType, resourceID string, detail any) {
	encoded, _ := json.Marshal(detail)
	_, _ = a.db.Exec(ctx, `INSERT INTO audit_logs(actor_id,action,resource_type,resource_id,detail) VALUES(NULL,$1,$2,$3,$4)`, action, resourceType, resourceID, encoded)
}
