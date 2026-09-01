import { describe, expect, it } from 'vitest'
import { filterInclusionMembers, inclusionMemberIDsWithinLimit } from './ReportInclusionSettings'
import type { ReportInclusionMember } from './types'

const members: ReportInclusionMember[] = [
  { id: 1, username: 'kim', displayName: '김하나', organizationName: '플랫폼팀' },
  { id: 2, username: 'lee', displayName: '이두리', organizationName: '서비스팀' },
]

describe('팀원 포함 설정 검색', () => {
  it('이름·아이디·조직으로 찾는다', () => {
    expect(filterInclusionMembers(members, '김하나').map(member => member.id)).toEqual([1])
    expect(filterInclusionMembers(members, 'LEE').map(member => member.id)).toEqual([2])
    expect(filterInclusionMembers(members, '플랫폼 팀').map(member => member.id)).toEqual([1])
  })

  it('공백 검색은 서버가 정한 순서를 보존한다', () => {
    expect(filterInclusionMembers(members, '  ')).toEqual(members)
  })

  it('일치하지 않으면 빈 목록이다', () => {
    expect(filterInclusionMembers(members, '없는사람')).toEqual([])
  })

  it('전체 선택도 서버가 알린 상한을 넘지 않는다', () => {
    expect(inclusionMemberIDsWithinLimit(members, 1)).toEqual([1])
    expect(inclusionMemberIDsWithinLimit(members, 0)).toEqual([])
  })
})
