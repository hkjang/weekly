import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import IncludedReportMaterials from './IncludedReportMaterials'
import type { IncludedReportMaterial } from './types'

describe('팀원 주간보고 읽기 전용 자료', () => {
  it('작성된 내용은 작성자와 함께, 미작성 선택은 빈 상태로 표시한다', () => {
    const materials: IncludedReportMaterial[] = [{
      userId: 2, username: 'member', displayName: '팀원', organizationName: '플랫폼팀',
      reportId: 20, status: 'DRAFT', summary: '작성 중 요약', items: [{
        category: '개발', title: '팀원 업무', currentResult: '구현', nextPlan: '검증',
        issue: '', managementAsk: '', progress: 60, sortOrder: 0,
      }],
    }, {
      userId: 3, username: 'missing', displayName: '미작성자', organizationName: '서비스팀',
      summary: '', items: [],
    }]
    const markup = renderToStaticMarkup(<IncludedReportMaterials materials={materials}/>)
    expect(markup).toContain('팀원 업무')
    expect(markup).toContain('작성 중')
    expect(markup).toContain('@member')
    expect(markup).toContain('미작성자')
    expect(markup).toContain('해당 주차 보고서가 없습니다.')
    expect(markup).not.toContain('<input')
  })

  it('선택 자료가 없으면 빈 상자를 만들지 않는다', () => {
    expect(renderToStaticMarkup(<IncludedReportMaterials materials={[]}/>)).toBe('')
  })
})
