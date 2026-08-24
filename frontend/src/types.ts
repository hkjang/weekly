export type Role = 'USER' | 'TEAM_LEADER' | 'ORG_MANAGER' | 'ADMIN'
export type ReportStatus = 'DRAFT' | 'SUBMITTED' | 'REVISION_REQUESTED' | 'APPROVED' | 'CLOSED'

export interface BuildInfo { version: string; commit: string; builtAt: string }
export interface User { id: number; username: string; displayName: string; email: string; role: Role; organizationId?: number; keyVersion: number }
export interface SessionInfo { user: User; workflowEnabled: boolean; aiEnabled: boolean; currentWeekStart: string; serviceName: string; notice: string; build: BuildInfo }
export interface Providers { local: boolean; oidc: boolean; name: string; notice: string; build: BuildInfo }
export type ItemSourceKind = 'MANUAL' | 'CONFLUENCE' | 'PPTX' | 'AI_TEXT' | 'JIRA' | 'GIT' | 'CI' | 'ITSM' | 'API'
export interface ItemSource { kind: ItemSourceKind; reference?: string; title?: string; url?: string; detail?: string; occurredAt?: string }
export interface EvidenceUse {
  reportId: number; reportItemId: number; weekStart: string; title: string
  category: string; displayName: string; organizationName: string; detail?: string
}
export interface EvidenceUseView { kind: string; reference: string; title?: string; uses: EvidenceUse[]; total: number; limit: number }
/** What happened to last week's issue, answered once when this week's is blank. */
export type IssueOutcome = 'RESOLVED'
export interface ReportItem { id?: number; workItemId?: number; candidateId?: number; category: string; title: string; currentResult: string; nextPlan: string; issue: string; managementAsk: string; progress: number; sortOrder: number; sources?: ItemSource[]; issueOutcome?: IssueOutcome }
export interface PreviousPlanItem { workItemId?: number; category: string; title: string; matchKey: string; nextPlan: string; issue: string; progress: number; carryOver: boolean }
export interface PreviousPlan { reportId: number; weekStart: string; status: ReportStatus; items: PreviousPlanItem[] }
export interface ReportComment { id: number; userId: number; displayName: string; content: string; createdAt: string }
export type ReportSource = 'MANUAL' | 'AI_TEXT' | 'PPTX_IMPORT' | 'CONFLUENCE_AI' | 'CLONED' | 'API' | 'JIRA'
export interface Report { id: number; userId: number; username: string; displayName: string; weekStart: string; status: ReportStatus; sourceType: ReportSource; summary: string; version: number; submittedAt?: string; reviewedAt?: string; createdAt: string; updatedAt: string; items: ReportItem[]; comments: ReportComment[] }
export interface ReportListItem { id: number; userId: number; username: string; displayName: string; weekStart: string; status: ReportStatus; sourceType: ReportSource; summary: string; version: number; submittedAt?: string; updatedAt: string }
export interface AnalyticsOverview { weekStart: string; totalUsers: number; submittedUsers: number; submissionRate: number; statusCounts: Record<string, number>; openIssues: number; averageProgress: number }
export interface KeyView { id: number; name: string; prefix: string; keyVersion: number; scopes: string[]; lastUsedAt?: string; expiresAt?: string; createdAt: string }
export interface AdminUser extends User { managerId?: number; active: boolean; lastLoginAt?: string; createdAt: string }
/** A page of accounts with the size of the directory it came from. */
export interface AdminUserPage { items: AdminUser[]; total: number; limit: number; offset: number; query?: string; roles?: string[] }
export interface Organization { id: number; parentId?: number; name: string; code: string; userCount: number }
export interface Setting { key: string; value?: string; secret: boolean; configured: boolean; available: boolean; updatedAt: string }
export type AIReportItem = {
  category: string; title: string; currentResult: string; nextPlan: string; issue: string; progress: number
  confidence: number; categoryConfidence?: number; sourceSlides?: number[]
}
export interface AIWeeklyResult { summary: string; weekStart: string; dateConfidence: number; reportItems: AIReportItem[]; warnings: string[] }
export type ImportJobStatus = 'PENDING' | 'PROCESSING' | 'READY' | 'PARTIAL' | 'FAILED' | 'CONFIRMED'
export type ImportFileStatus = 'QUEUED' | 'PROCESSING' | 'READY' | 'NEEDS_REVIEW' | 'FAILED' | 'DUPLICATE' | 'CONFIRMED' | 'SKIPPED'
export interface ImportFile {
  id: number; originalFilename: string; fileHash: string; sizeBytes: number; status: ImportFileStatus
  detectedWeekStart: string; detectedWeekEnd: string; confidence: number; dateSource: string
  result?: AIWeeklyResult; errorMessage?: string; duplicateOf?: number; reportId?: number
  conflictReportId?: number; conflictReportStatus?: string; createdAt: string; analyzedAt?: string; confirmedAt?: string
}
export interface ImportJob {
  id: number; status: ImportJobStatus; totalFiles: number; processedFiles: number; failedFiles: number
  createdAt: string; startedAt?: string; completedAt?: string; confirmedAt?: string; files?: ImportFile[]
}
export interface ConfluenceSource { pageId: string; title: string; spaceKey: string; pageUrl: string; pageVersion: number; activityType: 'CREATED' | 'MODIFIED' | 'CREATED_AND_MODIFIED'; sourceUpdatedAt?: string }
export interface ConfluenceCandidate {
  id: number; weekStart: string; normalizedTitle: string; category: string; currentResult: string; nextPlan: string; issue: string
  confidence: number; ruleScore: number; status: 'DETECTED' | 'ACCEPTED' | 'IGNORED' | 'MERGED' | 'REMOVED'; userEdited: boolean
  sources: ConfluenceSource[]; createdAt: string; updatedAt: string
}
export interface ConfluenceCandidateResponse {
  enabled: boolean; weekStart: string; candidates: ConfluenceCandidate[]
  sync: { status: string; lastSuccessAt?: string; lastAttemptAt?: string; errorMessage?: string }
}
export interface ConfluenceMapping {
  userId: number; username: string; displayName: string; email: string; externalUsername?: string; mappingSource?: 'EXPLICIT' | 'EMAIL_LOCALPART' | 'USERNAME'
  active?: boolean; suggestedUsername: string; suggestionSource: 'EMAIL_LOCALPART' | 'USERNAME'
}
export interface WorkItemWeek { weekStart: string; reportId: number; itemIds: number[]; progress: number; currentResult: string; nextPlan: string; issue: string; managementAsk: string }
export interface WorkItem {
  id: number; title: string; category: string; userId: number; displayName: string; dueDate?: string
  firstWeek: string; lastWeek: string; reportedWeeks: number; ageWeeks: number; silentWeeks: number
  progress: number; startProgress: number; progressGain: number
  stalledWeeks: number; issueWeeks: number; repeatedPlan: number
  completed: boolean; stalled: boolean; atRisk: boolean; carryover: boolean
  latestIssue: string; latestManagementAsk: string
  /**
   * Only the detail response carries these. The list leaves them out because
   * they are the bulk of the payload and nothing in the table reads them; open
   * one task and GET /api/v1/work-items/{id} brings its history.
   */
  weeks?: WorkItemWeek[]
  forecast: CompletionForecast; dueOutlook: DueOutlook; agreedDue?: AgreedDue
}
export interface WorkItemPage { items: WorkItem[]; total: number; limit: number; offset: number }

export type ChangeKind = 'NEW' | 'RESUMED' | 'COMPLETED' | 'PROGRESSED' | 'REGRESSED' | 'STALLED' | 'SILENT' | 'STEADY' | 'ABSENT'
export interface ChangeEntry {
  workItemId: number; title: string; category: string; displayName: string; organizationName: string
  kind: ChangeKind; note: string; detail: string; progress: number; progressDelta: number
}
export interface ChangeGroup { kind: ChangeKind; title: string; count: number; entries: ChangeEntry[] }
export interface ChangeSummary { week: string; previousWeek: string; scope: string; reported: number; changed: number; groups: ChangeGroup[] }

export interface QualityFinding { rule: string; severity: 'WARN' | 'INFO'; title: string; message: string }
export interface QualityReport { week: string; checked: number; findings: QualityFinding[] }

export interface AnalysisTerm { term: string; count: number; documents: number; weight: number; delta: number; phrase: boolean; variants?: string[] }
export interface KeywordAnalytics {
  start: string; end: string; weeks: number; field: string; documents: number; reports: number
  terms: AnalysisTerm[]; comparedStart: string; comparedEnd: string; comparedReports: number
}
export interface OrganizationAnalyticsRow {
  organizationId: number; name: string; members: number; reports: number; expectedReports: number
  submissionRate: number; items: number; completedItems: number; completionRate: number
  issueItems: number; issueRate: number; averageProgress: number
}
export interface OrganizationAnalytics { start: string; end: string; weeks: number; organizations: OrganizationAnalyticsRow[] }
export interface ParticipationWeek {
  weekStart: string; activeUsers: number; reports: number; submitted: number
  onTime: number; late: number; submissionRate: number; onTimeRate: number
}
export interface MissingReporter { userId: number; displayName: string; username: string; organization: string; missedWeeks: number; lastWeek: string }
export interface DeadlineRule { days: number; hour: number; timezone: string; label: string }
export interface ParticipationAnalytics {
  start: string; end: string; weeks: number; activeUsers: number
  trend: ParticipationWeek[]; missing: MissingReporter[]; missingTotal: number; missingLimit: number; deadline: DeadlineRule
}

export interface SearchMatch { field: string; label: string; title?: string; snippet: string }
export interface SearchHit {
  approximate?: boolean; semantic?: boolean; reportId: number; userId: number; displayName: string; weekStart: string
  status: ReportStatus; sourceType: ReportSource; matches: SearchMatch[]; score: number
}
export interface SearchResponse { query: string; terms: string[]; hits: SearchHit[]; truncated: boolean; fuzzy?: boolean; semantic?: boolean }

export type AttachmentPlacement = 'BEFORE' | 'AFTER'
export interface ReportAttachment {
  id: number; filename: string; caption: string; placement: AttachmentPlacement
  sortOrder: number; sizeBytes: number; width: number; height: number; createdAt: string
  /** false when the stored file is gone; the row survives but the image cannot be shown. */
  available?: boolean
}

export type PeriodKind = 'MONTH' | 'QUARTER' | 'HALF' | 'YEAR'
export type RollupScope = 'SELF' | 'TEAM'
export type HighlightSeverity = 'RISK' | 'WATCH' | 'GOOD' | 'INFO'
/**
 * What the reported pace says about a deadline that was set. The verdict never
 * travels without projectedLow/projectedHigh and the note that names the paces,
 * because a label nobody can check is one nobody should act on.
 */
/**
 * A deadline a meeting already agreed, recorded as a decision follow-up, on
 * work that has no deadline of its own. Offered rather than copied: a follow-up
 * is not always the whole task, and promoting one silently would claim
 * something the meeting did not say.
 */
export interface AgreedDue {
  dueDate: string; title: string; decidedBy: string; decidedOn: string; followUp: string; decisionId: number
}
/**
 * The same arithmetic as DueOutlook, run against the boundary the period
 * report is already about. It needs no deadline entered anywhere, and it is
 * not a commitment: a deadline says somebody promised, a period end says the
 * report stops here.
 */
export interface PeriodOutlook {
  kind: 'NONE' | 'LANDS' | 'SPLIT' | 'SHORT' | 'FINISHED'
  periodEnd?: string; weeksLeft: number; projectedLow: number; projectedHigh: number; note: string
}
export interface DueOutlook {
  kind: 'NONE' | 'FINISHED' | 'UNKNOWN' | 'ON_TRACK' | 'SPLIT' | 'AT_RISK' | 'OVERDUE'
  dueDate?: string; weeksLeft: number; projectedLow: number; projectedHigh: number; note: string
}
export interface RollupItemWeek { weekStart: string; progress: number; hasIssue: boolean }
/**
 * What the reported progress implies about the finish, and the paces it was
 * computed from. There is no deadline to compare against, so this never claims
 * one: DONE, STALLED (no forward movement, so no date exists), INSUFFICIENT
 * (too few weeks to say anything), DISTANT (over a year, where a week count
 * would be false precision) or PROJECTED, which is a range and not a point.
 */
export interface CompletionForecast {
  kind: 'DONE' | 'STALLED' | 'INSUFFICIENT' | 'PROJECTED' | 'DISTANT'
  earliestWeeks?: number; latestWeeks?: number; earliestWeek?: string; latestWeek?: string
  overallPerWeek: number; recentPerWeek: number; basedOnWeeks: number; note: string
}
export interface RollupItem {
  key: string; category: string; title: string; currentResult: string; nextPlan: string; issue: string
  managementAsk: string; progress: number; startProgress: number; firstWeek: string; lastWeek: string; weekCount: number; issueWeeks: number
  owners: string[]
  /**
   * Only the leading `timelineItems` rows carry this. The rest are table rows
   * and the chart never plots them, so their weekly text is not sent.
   */
  weeks?: RollupItemWeek[]
  mergedTitles: string[]
  completed: boolean; stalled: boolean; atRisk: boolean; carryover: boolean; duplicatesCut: number
  forecast: CompletionForecast; periodOutlook: PeriodOutlook
}
export interface RollupCategory { name: string; items: number; completed: number; averageProgress: number; share: number; issueItems: number }
export interface RollupContributor { userId: number; displayName: string; reports: number; items: number; completed: number; issueItems: number; averageProgress: number }
export interface RollupHighlight { severity: HighlightSeverity; category: 'DELIVERY' | 'RISK' | 'COVERAGE' | 'PORTFOLIO'; title: string; detail: string }
export interface RollupWeekPoint {
  weekStart: string; reports: number; contributors: number; activeItems: number
  completedItems: number; notStartedItems: number; issueItems: number; averageProgress: number
}
export interface RollupInsights {
  totalItems: number; completedItems: number; inProgressItems: number; notStartedItems: number
  completionRate: number; averageProgress: number; progressGain: number
  continuingItems: number; oneOffItems: number; stalledItems: number; noLandingItems: number; missesPeriod: number; carryoverItems: number
  issueItems: number; persistentIssues: number; askItems: number
  expectedWeeks: number; reportedWeeks: number; reportCoverage: number
  sourceReports: number; sourceItems: number; duplicatesCut: number; mergedTitles: number; dedupRate: number
}
export interface Rollup {
  kind: PeriodKind; period: string; label: string; start: string; end: string
  scope: RollupScope; scopeLabel: string; summary: string
  insights: RollupInsights; highlights: RollupHighlight[]; items: RollupItem[]
  issueClearance: { resolved: number; medianWeeks: number; longestWeeks: number; longestTitle?: string }
  categories: RollupCategory[]; contributors: RollupContributor[]; trend: RollupWeekPoint[]
  weeks: string[]
  decisions: Decision[]; decisionTotal: number; openDecisions: number; decisionLimit: number
  generatedAt: string
}

export interface ConfluenceSyncStatus {
  enabled: boolean; status: string; lastSuccessAt?: string; lastAttemptAt?: string; currentStartedAt?: string; errorMessage: string
  pagesScanned: number; pagesChanged: number; candidatesCreated: number; pagesFailed: number; mappedUsers: number; unmappedUsers: number
  recentErrors: { id: number; pageId?: string; phase: string; statusCode?: number; message: string; createdAt: string }[]
}

/** Coverage of the optional semantic search index. */
export interface EmbeddingStatus {
  vectorAvailable: boolean; enabled: boolean; model: string
  items: number; embedded: number; stale: number; reason?: string
}

// --- 회의 모드 · 경영 요약 · 업무 인사이트 -----------------------------------

export interface MeetingEntry {
  workItemId: number; title: string; category: string; displayName: string
  organizationName: string; detail: string; note: string
  progress: number; progressDelta: number; weeks: number
}
export interface MeetingSection {
  key: string; title: string; purpose: string; entries: MeetingEntry[]
  /** How many belong here, against the `limit` that actually arrived. */
  total: number; limit: number
}
export interface MeetingView {
  week: string; previousWeek: string; scope: RollupScope
  people: number; workItems: number; sections: MeetingSection[]
}

export type DigestKind = 'DECISION' | 'RISK' | 'DUPLICATE' | 'PROGRESS'
export type DigestGroundKind = 'DECISION' | 'ISSUE' | 'STALLED' | 'BLOCKED' | 'SILENT' | 'DUPLICATE' | 'DONE'
export interface DigestGround { kind: DigestGroundKind; text: string; points: number }
export interface DigestEntry {
  kind: DigestKind; score: number; title: string; workItemId: number
  displayName: string; organizationName: string
  headline: string; detail: string; grounds: DigestGround[]
}
export interface DigestView { weeks: number; since: string; scope: RollupScope; considered: number; entries: DigestEntry[] }

export interface WorkRef {
  workItemId: number; title: string; category: string; userId: number; displayName: string
  organizationId?: number; organizationName: string; progress: number; lastWeek: string; completed: boolean
}
export interface WorkLink {
  similarity: number; sharedTerms: string[]; crossOrganization: boolean
  duplicateCandidate: boolean; overlapWeeks: number; left: WorkRef; right: WorkRef; reason: string
}
export interface CollaborationEdge {
  leftOrganization: string; rightOrganization: string
  sharedWork: number; people: number; topics: string[]
}
export interface RecurringWork extends WorkRef {
  reportedWeeks: number; ageWeeks: number; cadencePercent: number
  progressGain: number; issueWeeks: number; reason: string
}
export interface WorkGraphView {
  weeks: number; since: string; workItems: number
  similar: WorkLink[]; similarTotal: number; duplicates: WorkLink[]; duplicateTotal: number
  collaboration: CollaborationEdge[]; recurring: RecurringWork[]; recurringTotal: number; bottlenecks: Bottleneck[]
}

export interface WorkSearchHit extends WorkRef {
  score: number; semantic: boolean; ageWeeks: number; issueWeeks: number; resolved: boolean
  matched: string[]; issue: string; resolution: string; why: string
}
export interface WorkSearchResponse { query: string; terms: string[]; semantic: boolean; hits: WorkSearchHit[] }

export interface HandoverIssue { week: string; text: string; resolved: boolean }
export interface ImportJobListView { items: ImportJob[]; total: number; limit: number; offset: number }
export interface Bottleneck {
  workItemId: number; title: string; displayName: string; organizationName: string
  progress: number; lastWeek: string; blocked: number; crossOrganization: number; waiting: string[]
}
export interface WorkItemLink {
  id: number; note: string; ready: boolean
  workItemId: number; title: string; displayName: string; organizationName: string
  progress: number; completed: boolean; lastWeek: string; createdAt: string
}
export interface WorkItemLinkView { blockers: WorkItemLink[]; blocking: WorkItemLink[] }
/**
 * Naming somebody else's task in order to declare you are waiting on it.
 * Carries only what the declaration itself will show — no issue or resolution
 * text from anyone's report.
 */
export interface WorkLookupHit { workItemId: number; title: string; displayName: string; organizationName: string }
export interface WorkLookupResponse { query: string; hits: WorkLookupHit[]; total: number; limit: number }
export interface DecisionCandidate {
  title: string; decidedBy: string; decidedOn: string; rationale: string
  followUp: string; confidence: number; evidence: string
}
export interface DecisionSuggestion {
  candidates: DecisionCandidate[]; caveat: string; weeks: number; scannedCharacters: number
}
export interface OpenFollowUp {
  decisionId: number; workItemId: number; workTitle: string; category: string
  title: string; decidedBy: string; decidedOn: string; followUp: string
  dueDate?: string; overdue: boolean
}
export type DecisionStatus = 'OPEN' | 'DONE' | 'SUPERSEDED'
export interface Decision {
  id: number; workItemId: number; workTitle?: string; title: string; decidedBy: string; decidedOn: string
  rationale: string; followUp: string; dueDate?: string; status: DecisionStatus
  supersedesId?: number; recordedByName: string; createdAt: string; updatedAt: string
}
export interface DecisionInput {
  title: string; decidedBy: string; decidedOn: string; rationale: string
  followUp: string; dueDate: string; status: DecisionStatus; supersedesId?: number
}
export interface ReportListView { items: ReportListItem[]; total: number; limit: number; offset: number }
export interface TeamMember { id: number; displayName: string; organizationName: string; active: boolean; lastWeek: string }
export interface HandoverWeek { week: string; progress: number; issue?: boolean }
export interface HandoverItem {
  workItemId: number; title: string; category: string; organizationName: string
  firstWeek: string; lastWeek: string; ageWeeks: number; reportedWeeks: number
  progress: number; completed: boolean; stalled: boolean
  openIssue: string; openAsk: string; nextPlan: string
  issueHistory: HandoverIssue[]; milestones: string[]; relatedWork: WorkRef[]; caution: string
  track: HandoverWeek[]; decisions: Decision[]
}
export interface HandoverView { userId: number; displayName: string; active: number; completed: number
  openDecisions: number; overdueDecisions: number; items: HandoverItem[] }
