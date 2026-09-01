import { StatusBadge } from './components'
import type { IncludedReportMaterial } from './types'

/**
 * Team-member reports selected as supporting material for somebody else's
 * weekly report. They are deliberately rendered outside Report.items: putting
 * them in the editable array would turn another person's writing into the
 * viewer's own work on the next save.
 */
export default function IncludedReportMaterials({ materials, heading = true }: {
  materials: IncludedReportMaterial[]
  heading?: boolean
}) {
  if (!materials.length) return null
  return <section className="included-materials" aria-label="팀원 주간보고 자료">
    {heading && <div className="included-materials-head">
      <div><h3>팀원 주간보고 자료</h3><p>개인 설정에서 선택한 팀원의 같은 주차 보고입니다.</p></div>
      <span className="muted-chip">{materials.length}명</span>
    </div>}
    <p className="included-materials-note">같은 주차의 읽기 전용 자료이며 작성 중인 보고서도 포함합니다. 내 업무 항목이나 팀원의 원본 보고서를 변경하지 않습니다.</p>
    <div className="included-material-list">{materials.map(material => {
      const hasReport = !!material.reportId
      return <article className={`included-material${hasReport ? '' : ' missing'}`} key={material.userId}>
        <header>
          <div><strong>{material.displayName}</strong><small>{[
            material.organizationName,
            material.username ? `@${material.username}` : '',
          ].filter(Boolean).join(' · ') || '조직 정보 없음'}</small></div>
          {material.status ? <StatusBadge status={material.status}/> : <span className="muted-chip">미작성</span>}
        </header>
        {!hasReport ? <p className="included-material-empty">해당 주차 보고서가 없습니다.</p> : <>
          <p className="included-material-summary">{material.summary.trim() || '주간 요약이 없습니다.'}</p>
          {material.items.length ? <div className="included-material-items">{material.items.map((item, index) => <section key={item.id ?? `${material.userId}-${index}`}>
            <h4>{item.title || `업무 ${index + 1}`} <small>{item.category ? `${item.category} · ` : ''}진척 {item.progress}%</small></h4>
            <dl>
              {item.currentResult.trim() && <><dt>금주 실적</dt><dd>{item.currentResult}</dd></>}
              {item.nextPlan.trim() && <><dt>차주 계획</dt><dd>{item.nextPlan}</dd></>}
              {item.issue.trim() && <><dt>이슈</dt><dd>{item.issue}</dd></>}
              {(item.managementAsk ?? '').trim() && <><dt>상위 조직 요청</dt><dd>{item.managementAsk}</dd></>}
            </dl>
          </section>)}</div> : <p className="included-material-empty">작성된 업무 항목이 없습니다.</p>}
        </>}
      </article>
    })}</div>
  </section>
}
