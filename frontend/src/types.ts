export type Role = 'USER' | 'TEAM_LEADER' | 'ORG_MANAGER' | 'ADMIN'
export type ReportStatus = 'DRAFT' | 'SUBMITTED' | 'REVISION_REQUESTED' | 'APPROVED' | 'CLOSED'

export interface BuildInfo { version: string; commit: string; builtAt: string }
export interface User { id: number; username: string; displayName: string; email: string; role: Role; organizationId?: number; keyVersion: number }
export interface SessionInfo { user: User; workflowEnabled: boolean; aiEnabled: boolean; currentWeekStart: string; serviceName: string; notice: string; build: BuildInfo }
export interface Providers { local: boolean; oidc: boolean; name: string; notice: string; build: BuildInfo }
export interface ReportItem { id?: number; candidateId?: number; category: string; title: string; currentResult: string; nextPlan: string; issue: string; progress: number; sortOrder: number }
export interface ReportComment { id: number; userId: number; displayName: string; content: string; createdAt: string }
export type ReportSource = 'MANUAL' | 'AI_TEXT' | 'PPTX_IMPORT' | 'CONFLUENCE_AI' | 'CLONED' | 'API' | 'JIRA'
export interface Report { id: number; userId: number; username: string; displayName: string; weekStart: string; status: ReportStatus; sourceType: ReportSource; summary: string; version: number; submittedAt?: string; reviewedAt?: string; createdAt: string; updatedAt: string; items: ReportItem[]; comments: ReportComment[] }
export interface ReportListItem { id: number; userId: number; username: string; displayName: string; weekStart: string; status: ReportStatus; sourceType: ReportSource; summary: string; version: number; submittedAt?: string; updatedAt: string }
export interface AnalyticsOverview { weekStart: string; totalUsers: number; submittedUsers: number; submissionRate: number; statusCounts: Record<string, number>; openIssues: number; averageProgress: number }
export interface KeyView { id: number; name: string; prefix: string; keyVersion: number; scopes: string[]; lastUsedAt?: string; expiresAt?: string; createdAt: string }
export interface AdminUser extends User { managerId?: number; active: boolean; lastLoginAt?: string; createdAt: string }
export interface Organization { id: number; parentId?: number; name: string; code: string; userCount: number }
export interface Setting { key: string; value?: string; secret: boolean; configured: boolean; available: boolean; updatedAt: string }
export type AIReportItem = Omit<ReportItem, 'id' | 'sortOrder'> & { confidence: number }
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
export interface ConfluenceSyncStatus {
  enabled: boolean; status: string; lastSuccessAt?: string; lastAttemptAt?: string; currentStartedAt?: string; errorMessage: string
  pagesScanned: number; pagesChanged: number; candidatesCreated: number; pagesFailed: number; mappedUsers: number; unmappedUsers: number
  recentErrors: { id: number; pageId?: string; phase: string; statusCode?: number; message: string; createdAt: string }[]
}
