package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// A personal selection is intentionally bounded. The ordinary profile screen
// is not a bulk directory-management API, and an accidental response containing
// every numeric id in a large installation should not turn one preference save
// into an unbounded transaction. Five hundred matches the largest report page
// the service already supports.
const maxReportInclusionMembers = 500

type reportInclusionMember struct {
	ID               int64  `json:"id"`
	Username         string `json:"username"`
	DisplayName      string `json:"displayName"`
	OrganizationName string `json:"organizationName"`
}

type reportInclusionPreferenceView struct {
	Available         bool                    `json:"available"`
	MaxMembers        int                     `json:"maxMembers"`
	SelectedMemberIDs []int64                 `json:"selectedMemberIds"`
	Members           []reportInclusionMember `json:"members"`
}

// includedReportMaterial is deliberately not a reportItem. It remains owned by
// the selected writer and is nested read-only beside the owner's report. Were
// it inserted into report_items, organisation rollups would count the same work
// once for its writer and once for every leader who selected that writer.
type includedReportMaterial struct {
	UserID           int64        `json:"userId"`
	Username         string       `json:"username"`
	DisplayName      string       `json:"displayName"`
	OrganizationName string       `json:"organizationName"`
	ReportID         *int64       `json:"reportId,omitempty"`
	Status           string       `json:"status,omitempty"`
	Summary          string       `json:"summary"`
	Version          int          `json:"version,omitempty"`
	UpdatedAt        *time.Time   `json:"updatedAt,omitempty"`
	Items            []reportItem `json:"items"`
}

type includedReportMaterialsView struct {
	WeekStart string                   `json:"weekStart"`
	Materials []includedReportMaterial `json:"materials"`
}

func canIncludeTeamReports(role string) bool {
	return role == "TEAM_LEADER" || role == "ORG_MANAGER" || role == "ADMIN"
}

// reportInclusionPreferenceFor returns only choices that are valid now. Rows
// are retained across a temporary demotion, deactivation or organisation move,
// but an old preference is never authority to read a report after its scope has
// changed.
func (a *App) reportInclusionPreferenceFor(ctx context.Context, p *principal) (reportInclusionPreferenceView, error) {
	view := reportInclusionPreferenceView{
		Available:         p != nil && canIncludeTeamReports(p.Role),
		MaxMembers:        maxReportInclusionMembers,
		SelectedMemberIDs: []int64{},
		Members:           []reportInclusionMember{},
	}
	if !view.Available || (p.Role != "ADMIN" && p.OrganizationID == nil) {
		return view, nil
	}

	args := []any{p.ID, p.ID}
	query := `SELECT u.id,u.username,u.display_name,coalesce(o.name,''),selection.member_user_id IS NOT NULL
		FROM users u
		LEFT JOIN organizations o ON o.id=u.organization_id
		LEFT JOIN user_report_inclusions selection
		  ON selection.owner_user_id=$1 AND selection.member_user_id=u.id
		WHERE u.active=true AND u.id<>$2`
	if p.Role != "ADMIN" {
		args = append(args, *p.OrganizationID)
		query += ` AND u.organization_id IN ` + orgSubtree(len(args))
	}
	query += ` ORDER BY coalesce(o.name,''),u.display_name,u.username,u.id`

	rows, err := a.db.Query(ctx, query, args...)
	if err != nil {
		return reportInclusionPreferenceView{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var member reportInclusionMember
		var selected bool
		if err := rows.Scan(&member.ID, &member.Username, &member.DisplayName,
			&member.OrganizationName, &selected); err != nil {
			return reportInclusionPreferenceView{}, err
		}
		view.Members = append(view.Members, member)
		if selected {
			view.SelectedMemberIDs = append(view.SelectedMemberIDs, member.ID)
		}
	}
	return view, rows.Err()
}

func (a *App) myReportInclusions(w http.ResponseWriter, r *http.Request) {
	view, err := a.reportInclusionPreferenceFor(r.Context(), currentPrincipal(r.Context()))
	if err != nil {
		a.logger.Error("read report inclusion preferences", "error", err,
			"trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "주간보고 포함 팀원 설정을 읽을 수 없습니다.")
		return
	}
	writeData(w, http.StatusOK, view)
}

func normaliseReportInclusionIDs(values []int64) ([]int64, error) {
	seen := make(map[int64]struct{}, len(values))
	for _, value := range values {
		if value <= 0 {
			return nil, errors.New("invalid member id")
		}
		seen[value] = struct{}{}
	}
	if len(seen) > maxReportInclusionMembers {
		return nil, errors.New("too many members")
	}
	result := make([]int64, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func (a *App) updateMyReportInclusions(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r.Context())
	var input struct {
		MemberIDs *[]int64 `json:"memberIds"`
	}
	sent, ok := decodeJSONFields(w, r, &input)
	if !ok {
		return
	}
	if !sent["memberids"] || input.MemberIDs == nil {
		writeError(w, http.StatusBadRequest, "INVALID_REPORT_INCLUSIONS", "포함할 팀원 목록이 필요합니다.")
		return
	}
	memberIDs, err := normaliseReportInclusionIDs(*input.MemberIDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REPORT_INCLUSIONS",
			fmt.Sprintf("팀원 식별자는 양수여야 하며 최대 %d명까지 선택할 수 있습니다.", maxReportInclusionMembers))
		return
	}

	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "주간보고 포함 팀원 설정을 저장할 수 없습니다.")
		return
	}
	defer tx.Rollback(r.Context())

	// The owner row serialises two profile tabs replacing the same set, and also
	// makes the role/organisation used for validation current rather than a
	// remembered browser value.
	var role string
	var organizationID *int64
	var active bool
	err = tx.QueryRow(r.Context(), `SELECT role,organization_id,active FROM users WHERE id=$1 FOR UPDATE`, p.ID).
		Scan(&role, &organizationID, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "현재 계정으로 이 설정을 저장할 수 없습니다.")
		return
	}
	if err != nil {
		a.logger.Error("lock report inclusion owner", "error", err, "userId", p.ID,
			"trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "주간보고 포함 팀원 설정을 저장할 수 없습니다.")
		return
	}
	if !active {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "현재 계정으로 이 설정을 저장할 수 없습니다.")
		return
	}
	if len(memberIDs) > 0 && !canIncludeTeamReports(role) {
		writeError(w, http.StatusForbidden, "REPORT_INCLUSION_ROLE_REQUIRED", "팀원 보고서 포함은 팀장 이상만 설정할 수 있습니다.")
		return
	}

	if len(memberIDs) > 0 {
		args := []any{p.ID, memberIDs}
		query := `SELECT u.id FROM users u
			WHERE u.id=ANY($2::bigint[]) AND u.active=true AND u.id<>$1`
		switch role {
		case "ADMIN":
		case "TEAM_LEADER", "ORG_MANAGER":
			if organizationID == nil {
				writeError(w, http.StatusForbidden, "REPORT_INCLUSION_MEMBER_FORBIDDEN", "활성 상태이며 담당 조직 범위 안에 있는 팀원만 선택할 수 있습니다.")
				return
			}
			args = append(args, *organizationID)
			query += ` AND u.organization_id IN ` + orgSubtree(len(args))
		default:
			// The non-empty ordinary-user case was handled above. Keeping this
			// default closed protects an unknown role added later.
			writeError(w, http.StatusForbidden, "REPORT_INCLUSION_ROLE_REQUIRED", "팀원 보고서 포함은 팀장 이상만 설정할 수 있습니다.")
			return
		}
		query += ` FOR SHARE OF u`
		rows, queryErr := tx.Query(r.Context(), query, args...)
		if queryErr != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "선택한 팀원 범위를 확인할 수 없습니다.")
			return
		}
		valid := 0
		for rows.Next() {
			var ignored int64
			if queryErr = rows.Scan(&ignored); queryErr != nil {
				break
			}
			valid++
		}
		if queryErr == nil {
			queryErr = rows.Err()
		}
		rows.Close()
		if queryErr != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "선택한 팀원 범위를 확인할 수 없습니다.")
			return
		}
		if valid != len(memberIDs) {
			// One answer for absent, inactive, self and out-of-scope identifiers:
			// a preference API must not become an account directory probe.
			writeError(w, http.StatusForbidden, "REPORT_INCLUSION_MEMBER_FORBIDDEN", "활성 상태이며 담당 조직 범위 안에 있는 팀원만 선택할 수 있습니다.")
			return
		}
	}

	if _, err = tx.Exec(r.Context(), `DELETE FROM user_report_inclusions WHERE owner_user_id=$1`, p.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "주간보고 포함 팀원 설정을 저장할 수 없습니다.")
		return
	}
	if len(memberIDs) > 0 {
		if _, err = tx.Exec(r.Context(), `INSERT INTO user_report_inclusions(owner_user_id,member_user_id)
			SELECT $1,unnest($2::bigint[])`, p.ID, memberIDs); err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "주간보고 포함 팀원 설정을 저장할 수 없습니다.")
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "주간보고 포함 팀원 설정을 저장할 수 없습니다.")
		return
	}
	a.audit(r, p, "weekly.report_inclusions", "user", strconv.FormatInt(p.ID, 10), map[string]any{
		"memberIds": memberIDs,
		"count":     len(memberIDs),
	})
	// Authentication happened before the transaction waited for the owner row.
	// If an administrator changed this account while it waited, p is now an old
	// authority snapshot. Build the response from the role and organisation that
	// were locked and actually authorised this write, never from the stale one.
	effective := *p
	effective.Role = role
	effective.OrganizationID = organizationID
	view, err := a.reportInclusionPreferenceFor(r.Context(), &effective)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "저장한 주간보고 포함 팀원 설정을 다시 읽을 수 없습니다.")
		return
	}
	writeData(w, http.StatusOK, view)
}

// includedMaterialsFor resolves one owner's currently valid selection against
// the seven days one report week covers. It never calls loadReport, so a
// selected leader's own selections cannot recurse or form cycles.
func (a *App) includedMaterialsFor(ctx context.Context, ownerID int64, weekStart string, viewer *principal) ([]includedReportMaterial, error) {
	materials := []includedReportMaterial{}
	var ownerRole string
	var ownerOrganizationID *int64
	var ownerActive bool
	err := a.db.QueryRow(ctx, `SELECT role,organization_id,active FROM users WHERE id=$1`, ownerID).
		Scan(&ownerRole, &ownerOrganizationID, &ownerActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return materials, nil
	}
	if err != nil {
		return nil, err
	}
	if !ownerActive || !canIncludeTeamReports(ownerRole) {
		return materials, nil
	}
	if ownerRole != "ADMIN" && ownerOrganizationID == nil {
		return materials, nil
	}

	// A lateral join on the days covered rather than on the date, because this
	// answers "did this person report for this week" and the deck says out loud
	// what it is told. After the administrator moves the week start, the report
	// a member filed on the old grid covers these same days under a different
	// date — and weekIsFree will not let them file a second one on the new date.
	// An exact match called that member unwritten, so the PPTX put "해당 주차
	// 주간보고가 없습니다" next to their name in a meeting about a report they
	// had written. At most one report covers any seven days, so the ordering
	// only decides between rows written before weekIsFree did; the latest is the
	// one still being written in, the same choice currentReport makes.
	args := []any{ownerID, weekStart, ownerID}
	query := `SELECT u.id,u.username,u.display_name,coalesce(o.name,''),
			report.id,coalesce(report.status,''),coalesce(report.summary,''),
			coalesce(report.version,0),report.updated_at
		FROM user_report_inclusions selection
		JOIN users u ON u.id=selection.member_user_id
		LEFT JOIN organizations o ON o.id=u.organization_id
		LEFT JOIN LATERAL (SELECT report.id,report.status,report.summary,report.version,report.updated_at
			FROM weekly_reports report
			WHERE report.user_id=u.id AND ` + weekCoveringDays("report", 2) + `
			ORDER BY report.week_start DESC LIMIT 1) report ON true
		WHERE selection.owner_user_id=$1 AND u.active=true AND u.id<>$3`
	if ownerRole != "ADMIN" {
		args = append(args, *ownerOrganizationID)
		query += ` AND u.organization_id IN ` + orgSubtree(len(args))
	}

	// Usually anyone allowed to read the owner's report also covers the owner's
	// subtree. The exception is an administrator account placed inside somebody
	// else's subtree: that administrator may select the whole company. Intersect
	// with the viewer's current scope so nested material never broadens the
	// permission granted by the outer report.
	if viewer != nil && viewer.ID != ownerID && viewer.Role != "ADMIN" {
		if !canIncludeTeamReports(viewer.Role) || viewer.OrganizationID == nil {
			return materials, nil
		}
		args = append(args, *viewer.OrganizationID)
		query += ` AND u.organization_id IN ` + orgSubtree(len(args))
	}
	query += ` ORDER BY coalesce(o.name,''),u.display_name,u.username,u.id`

	rows, err := a.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	reportIndexes := map[int64]int{}
	reportIDs := []int64{}
	for rows.Next() {
		material := includedReportMaterial{Items: []reportItem{}}
		if err := rows.Scan(&material.UserID, &material.Username, &material.DisplayName,
			&material.OrganizationName, &material.ReportID, &material.Status,
			&material.Summary, &material.Version, &material.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		materials = append(materials, material)
		if material.ReportID != nil {
			reportIndexes[*material.ReportID] = len(materials) - 1
			reportIDs = append(reportIDs, *material.ReportID)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(reportIDs) == 0 {
		return materials, err
	}

	itemRows, err := a.db.Query(ctx, `SELECT report_id,id,work_item_id,category,title,current_result,
			next_plan,issue,management_ask,progress,sort_order
		FROM report_items WHERE report_id=ANY($1::bigint[]) ORDER BY report_id,sort_order,id`, reportIDs)
	if err != nil {
		return nil, err
	}
	defer itemRows.Close()
	for itemRows.Next() {
		var reportID int64
		var item reportItem
		if err := itemRows.Scan(&reportID, &item.ID, &item.WorkItemID, &item.Category,
			&item.Title, &item.CurrentResult, &item.NextPlan, &item.Issue,
			&item.ManagementAsk, &item.Progress, &item.SortOrder); err != nil {
			return nil, err
		}
		if index, ok := reportIndexes[reportID]; ok {
			materials[index].Items = append(materials[index].Items, item)
		}
	}
	return materials, itemRows.Err()
}

func (a *App) currentIncludedMaterials(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r.Context())
	week := currentWeekStart(time.Now().In(a.serviceLocation(r.Context())),
		a.setting(r.Context(), "workflow.week_start", "MONDAY"))
	weekStart := week.Format("2006-01-02")
	materials, err := a.includedMaterialsFor(r.Context(), p.ID, weekStart, p)
	if err != nil {
		a.logger.Error("load current included report materials", "error", err,
			"trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "포함할 팀원 주간보고 자료를 읽을 수 없습니다.")
		return
	}
	writeData(w, http.StatusOK, includedReportMaterialsView{WeekStart: weekStart, Materials: materials})
}

// reportItemsWithIncludedMaterials is the ephemeral flat projection used only
// by PPTX generation. Database report items remain untouched; labels keep the
// author's own work and each selected writer's material visibly separate.
func reportItemsWithIncludedMaterials(report *reportView) []reportItem {
	items := append([]reportItem(nil), report.Items...)
	for _, material := range report.IncludedMaterials {
		owner := material.DisplayName
		if strings.TrimSpace(material.Username) != "" {
			owner += " (" + material.Username + ")"
		}
		category := "선택 팀원 · " + owner
		if material.ReportID == nil {
			items = append(items, reportItem{Category: category, Title: "주간보고 미작성",
				CurrentResult: "해당 주차 주간보고가 없습니다.", SortOrder: len(items)})
			continue
		}
		summary := "보고 상태: " + reportStatusLabel(material.Status)
		if reportSummary := strings.TrimSpace(material.Summary); reportSummary != "" {
			summary += "\n" + reportSummary
		}
		items = append(items, reportItem{Category: category, Title: "주간 요약",
			CurrentResult: summary, SortOrder: len(items)})
		for _, source := range material.Items {
			copied := source
			copied.ID = 0
			copied.WorkItemID = nil
			copied.Sources = nil
			copied.Category = category
			if strings.TrimSpace(source.Category) != "" {
				copied.Category += " · " + strings.TrimSpace(source.Category)
			}
			copied.Title = source.Title
			copied.SortOrder = len(items)
			items = append(items, copied)
		}
	}
	return items
}
