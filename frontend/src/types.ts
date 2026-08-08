export type Role = 'USER' | 'TEAM_LEADER' | 'ORG_MANAGER' | 'ADMIN'
export type ReportStatus = 'DRAFT' | 'SUBMITTED' | 'REVISION_REQUESTED' | 'APPROVED' | 'CLOSED'

export interface BuildInfo { version: string; commit: string; builtAt: string }
export interface User { id: number; username: string; displayName: string; email: string; role: Role; organizationId?: number; keyVersion: number }
export interface SessionInfo { user: User; workflowEnabled: boolean; serviceName: string; notice: string; build: BuildInfo }
export interface Providers { local: boolean; oidc: boolean; name: string; notice: string; build: BuildInfo }
export interface ReportItem { id?: number; category: string; title: string; currentResult: string; nextPlan: string; issue: string; progress: number; sortOrder: number }
export interface ReportComment { id: number; userId: number; displayName: string; content: string; createdAt: string }
export interface Report { id: number; userId: number; username: string; displayName: string; weekStart: string; status: ReportStatus; summary: string; version: number; submittedAt?: string; reviewedAt?: string; createdAt: string; updatedAt: string; items: ReportItem[]; comments: ReportComment[] }
export interface ReportListItem { id: number; userId: number; username: string; displayName: string; weekStart: string; status: ReportStatus; summary: string; version: number; submittedAt?: string; updatedAt: string }
export interface AnalyticsOverview { weekStart: string; totalUsers: number; submittedUsers: number; submissionRate: number; statusCounts: Record<string, number>; openIssues: number; averageProgress: number }
export interface KeyView { id: number; name: string; prefix: string; keyVersion: number; scopes: string[]; lastUsedAt?: string; expiresAt?: string; createdAt: string }
export interface AdminUser extends User { managerId?: number; active: boolean; lastLoginAt?: string; createdAt: string }
export interface Organization { id: number; parentId?: number; name: string; code: string; userCount: number }
export interface Setting { key: string; value?: string; secret: boolean; configured: boolean; updatedAt: string }
